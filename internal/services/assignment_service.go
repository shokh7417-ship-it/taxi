package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/legal"
)

const assignmentLogErrMaxChars = 200

var (
	// ErrOfferNotFound means the driver tried to accept a request they were not offered.
	ErrOfferNotFound = errors.New("offer not found")
)

func assignmentTrunc(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max < 4 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func assignmentErrStr(err error) string {
	if err == nil {
		return ""
	}
	return assignmentTrunc(err.Error(), assignmentLogErrMaxChars)
}

// AssignmentService assigns requests to drivers and runs the expiry worker.
type AssignmentService struct {
	db         *sql.DB
	riderBot   *tgbotapi.BotAPI
	driverBot  *tgbotapi.BotAPI
	cfg        *config.Config
}

// NewAssignmentService returns an AssignmentService.
func NewAssignmentService(db *sql.DB, riderBot, driverBot *tgbotapi.BotAPI, cfg *config.Config) *AssignmentService {
	return &AssignmentService{db: db, riderBot: riderBot, driverBot: driverBot, cfg: cfg}
}

// TryAssign atomically assigns the request to the driver. Only one driver can accept (race-safe).
// Returns (true, tripID, nil) if assigned; (false, "", nil) if another driver already accepted.
func (s *AssignmentService) TryAssign(ctx context.Context, requestID string, driverUserID int64) (assigned bool, tripID string, err error) {
	if !legal.NewService(s.db).DriverHasActiveLegal(ctx, driverUserID) {
		return false, "", fmt.Errorf("assignment: driver %d missing active legal acceptances", driverUserID)
	}
	// Require that an offer exists for this (request, driver) pair. This prevents arbitrary accepts.
	var offerExists int
	if e := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM request_notifications
		WHERE request_id = ?1 AND driver_user_id = ?2 AND status = ?3
		LIMIT 1`,
		requestID, driverUserID, domain.NotificationStatusSent).Scan(&offerExists); e != nil {
		if e == sql.ErrNoRows {
			return false, "", ErrOfferNotFound
		}
		return false, "", e
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE ride_requests
		SET status = ?1, assigned_driver_user_id = ?2, assigned_at = datetime('now')
		WHERE id = ?3 AND status = ?4 AND expires_at > datetime('now')`,
		domain.RequestStatusAssigned, driverUserID, requestID, domain.RequestStatusPending)
	if err != nil {
		return false, "", err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return false, "", nil // another driver already accepted or request expired
	}
	var riderUserID int64
	err = s.db.QueryRowContext(ctx, `SELECT rider_user_id FROM ride_requests WHERE id = ?1`, requestID).Scan(&riderUserID)
	if err != nil {
		return false, "", err
	}
	// Mark this driver's notification as ACCEPTED (for dispatch log)
	_, _ = s.db.ExecContext(ctx, `UPDATE request_notifications SET status = ?1 WHERE request_id = ?2 AND driver_user_id = ?3`,
		domain.NotificationStatusAccepted, requestID, driverUserID)

	tripID = uuid.New().String()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO trips (id, request_id, driver_user_id, rider_user_id, status)
		VALUES (?1, ?2, ?3, ?4, ?5)`,
		tripID, requestID, driverUserID, riderUserID, domain.TripStatusWaiting)
	if err != nil {
		return false, "", err
	}
	log.Printf("dispatch_audit: request=%s accepted_by=%d trip_id=%s", requestID, driverUserID, tripID)

	var riderTelegramID int64
	err = s.db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, riderUserID).Scan(&riderTelegramID)
	if err == nil {
		chatID := riderTelegramID
		// Include driver info (phone first) so rider can contact the driver
		var driverPhone, carType, color, plate string
		var userPhone sql.NullString
		_ = s.db.QueryRowContext(ctx, `
			SELECT COALESCE(d.phone,''), COALESCE(d.car_type,''), COALESCE(d.color,''), COALESCE(d.plate,''), u.phone
			FROM drivers d JOIN users u ON u.id = d.user_id WHERE d.user_id = ?1`, driverUserID).
			Scan(&driverPhone, &carType, &color, &plate, &userPhone)
		phone := strings.TrimSpace(driverPhone)
		if phone == "" && userPhone.Valid && strings.TrimSpace(userPhone.String) != "" {
			phone = strings.TrimSpace(userPhone.String)
		}
		body := "🚗 Ҳайдовчи топилди!\n\nСизни қуйидаги ҳайдовчи олиб кетади:\n"
		if phone != "" {
			body += "📞 Телефон: " + phone + "\n"
		}
		if carType != "" {
			body += "🚗 " + carType
			if color != "" {
				body += ", " + color
			}
			body += "\n"
		} else if color != "" {
			body += "🚗 " + color + "\n"
		}
		if plate != "" {
			body += "🔢 " + plate + "\n"
		}
		msg := tgbotapi.NewMessage(chatID, body)
		if s.cfg.RiderMapURL != "" {
			riderMapURL := strings.TrimSuffix(s.cfg.RiderMapURL, "/") + "?trip_id=" + tripID
			msg.ReplyMarkup = riderMapWebAppKeyboard("📍 Ҳайдовчини кузатиш", riderMapURL)
		}
		if _, err := s.riderBot.Send(msg); err != nil {
			log.Printf("assignment_service: notify rider: %v", assignmentErrStr(err))
		}
		// Reply keyboard for trip-active state: Haydovchini kuzatish, Bekor qilish
		riderTripActiveKeyboard := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("📍 Ҳайдовчини кузатиш"),
				tgbotapi.NewKeyboardButton("❌ Бекор қилиш"),
			),
		)
		riderTripActiveKeyboard.ResizeKeyboard = true
		kbMsg := tgbotapi.NewMessage(chatID, "Ҳайдовчини харитада кузатинг ёки сафарни бекор қилишингиз мумкин.")
		kbMsg.ReplyMarkup = riderTripActiveKeyboard
		if _, err := s.riderBot.Send(kbMsg); err != nil {
			log.Printf("assignment_service: notify rider keyboard: %v", assignmentErrStr(err))
		}
	}

	notifRows, err := s.db.QueryContext(ctx, `
		SELECT chat_id, message_id FROM request_notifications
		WHERE request_id = ?1 AND driver_user_id != ?2`,
		requestID, driverUserID)
	if err != nil {
		return true, tripID, nil
	}
	defer notifRows.Close()
	for notifRows.Next() {
		var chatID int64
		var messageID int
		if err := notifRows.Scan(&chatID, &messageID); err != nil {
			continue
		}
		// Delete the order message instead of sending "So'rov allaqachon olindi".
		if s.driverBot != nil && messageID != 0 {
			del := tgbotapi.NewDeleteMessage(chatID, messageID)
			if _, err := s.driverBot.Request(del); err != nil {
				log.Printf("assignment_service: delete order message chat=%d msg=%d: %v", chatID, messageID, assignmentErrStr(err))
			}
		}
	}
	return true, tripID, nil
}

// RunExpiryWorker runs every 5 seconds: marks expired PENDING requests as EXPIRED and notifies each rider "Haydovchi topilmadi."
func (s *AssignmentService) RunExpiryWorker(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.expireRequests(ctx)
		}
	}
}

func (s *AssignmentService) expireRequests(ctx context.Context) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("assignment_service: begin tx: %v", assignmentErrStr(err))
		return
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		UPDATE ride_requests SET status = ?1
		WHERE status = ?2 AND expires_at <= datetime('now')
		RETURNING id, rider_user_id`,
		domain.RequestStatusExpired, domain.RequestStatusPending)
	if err != nil {
		log.Printf("assignment_service: expire update: %v", assignmentErrStr(err))
		return
	}
	defer rows.Close()

	var riderUserIDs []int64
	for rows.Next() {
		var id string
		var riderUserID int64
		if err := rows.Scan(&id, &riderUserID); err != nil {
			continue
		}
		riderUserIDs = append(riderUserIDs, riderUserID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("assignment_service: commit: %v", assignmentErrStr(err))
		return
	}

	for _, riderUserID := range riderUserIDs {
		var telegramID int64
		err := s.db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, riderUserID).Scan(&telegramID)
		if err != nil {
			continue
		}
		msg := tgbotapi.NewMessage(telegramID, "Ҳайдовчи топилмади.")
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Қайта қидириш", "search_again"),
			),
		)
		if _, err := s.riderBot.Send(msg); err != nil {
			log.Printf("assignment_service: notify rider expired: %v", assignmentErrStr(err))
		}
		// Restore main menu so rider has clear entry point
		mainMenu := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("🚕 Такси чақириш"),
				tgbotapi.NewKeyboardButton("ℹ️ Ёрдам"),
			),
		)
		mainMenu.ResizeKeyboard = true
		kbMsg := tgbotapi.NewMessage(telegramID, "Янги сўров учун «Такси чақириш» ни босинг.")
		kbMsg.ReplyMarkup = mainMenu
		if _, err := s.riderBot.Send(kbMsg); err != nil {
			log.Printf("assignment_service: rider main menu after expiry: %v", assignmentErrStr(err))
		}
	}
}

// RunRadiusExpansionWorker runs periodically: after RadiusExpansionMinutes, expands radius to ExpandedRadiusKm and re-broadcasts.
func (s *AssignmentService) RunRadiusExpansionWorker(ctx context.Context, matchSvc *MatchService) {
	if matchSvc == nil {
		return
	}
	expansionDelay := time.Duration(s.cfg.RadiusExpansionMinutes) * time.Minute
	if expansionDelay <= 0 {
		expansionDelay = 5 * time.Minute
	}
	tick := time.NewTicker(1 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.expandRadiusAndRebroadcast(ctx, matchSvc, expansionDelay)
		}
	}
}

func (s *AssignmentService) expandRadiusAndRebroadcast(ctx context.Context, matchSvc *MatchService, expansionDelay time.Duration) {
	cutoff := time.Now().Add(-expansionDelay)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM ride_requests
		WHERE status = ?1 AND expires_at > datetime('now')
		  AND (radius_expanded_at IS NULL)
		  AND radius_km < ?2
		  AND created_at <= ?3`,
		domain.RequestStatusPending, s.cfg.ExpandedRadiusKm, cutoff)
	if err != nil {
		log.Printf("assignment_service: radius expansion query: %v", assignmentErrStr(err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var requestID string
		if err := rows.Scan(&requestID); err != nil {
			continue
		}
		_, err := s.db.ExecContext(ctx, `
			UPDATE ride_requests SET radius_km = ?1, radius_expanded_at = datetime('now')
			WHERE id = ?2 AND status = ?3 AND radius_expanded_at IS NULL`,
			s.cfg.ExpandedRadiusKm, requestID, domain.RequestStatusPending)
		if err != nil {
			continue
		}
		log.Printf("assignment_service: expanded radius for request %s to %.1f km, re-broadcasting", requestID, s.cfg.ExpandedRadiusKm)
		if err := matchSvc.BroadcastRequest(ctx, requestID); err != nil {
			log.Printf("assignment_service: re-broadcast: %v", assignmentErrStr(err))
		}
	}
}

// riderMapWebAppKeyboard returns an inline keyboard with one Web App button (opens Telegram Mini App).
// Uses custom type because tgbotapi.InlineKeyboardButton does not expose web_app in this library version.
func riderMapWebAppKeyboard(buttonText, webAppURL string) riderMapInlineKeyboard {
	return riderMapInlineKeyboard{
		InlineKeyboard: [][]riderMapWebAppButton{{
			{Text: buttonText, WebApp: &riderMapWebAppInfo{URL: webAppURL}},
		}},
	}
}

type riderMapInlineKeyboard struct {
	InlineKeyboard [][]riderMapWebAppButton `json:"inline_keyboard"`
}
type riderMapWebAppButton struct {
	Text   string              `json:"text"`
	WebApp *riderMapWebAppInfo `json:"web_app,omitempty"`
}
type riderMapWebAppInfo struct {
	URL string `json:"url"`
}
