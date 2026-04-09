package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/legal"
	"taxi-mvp/internal/utils"
)

var (
	// ErrAdminOfferServiceUnavailable means MatchService cannot send offers (missing bot or service not wired).
	ErrAdminOfferServiceUnavailable = errors.New("offer service not configured")
	ErrAdminOfferUnknownRequest     = errors.New("unknown request_id")
	ErrAdminOfferUnknownDriver      = errors.New("unknown driver_id")
	ErrAdminOfferRequestNotAvail    = errors.New("request not available")
	ErrAdminOfferDriverNotElig      = errors.New("driver not eligible")
)

const (
	acceptCallbackPrefix   = "accept:"
	defaultDriverCooldown  = 5
	dispatchBatchSize      = 3  // send request to N nearest drivers per batch
	dispatchBatchWaitSec   = 60 // wait this many seconds for any driver in the batch to accept before trying next batch
	liveLocationOrderHint  = "\n\n📍 Жонли локация ёқилган бўлса буюртмалар тезроқ келади."
	// Live location considered active only when last_live_location_at within 90s (same as dispatch).
	liveLocationActiveSeconds = 90
	// DriverLocationFreshnessSeconds: only drivers with last_seen_at within this many seconds are eligible for dispatch.
	driverLocationFreshnessSeconds = 90

	// To prevent "output too large" issues, never log large slices verbatim.
	logSliceMaxItems = 20
	logMaxChars      = 180
)

func truncateLog(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	if maxChars < 4 {
		return s[:maxChars]
	}
	return s[:maxChars-3] + "..."
}

func sampleInt64(ids []int64, maxItems int) string {
	if len(ids) == 0 {
		return "[]"
	}
	n := len(ids)
	if maxItems > 0 && n > maxItems {
		n = maxItems
	}
	base := fmt.Sprintf("%v", ids[:n])
	if maxItems > 0 && len(ids) > n {
		base += fmt.Sprintf("+%d_more", len(ids)-n)
	}
	return truncateLog(base, logMaxChars)
}

func sampleString(ss []string, maxItems int) string {
	if len(ss) == 0 {
		return "[]"
	}
	n := len(ss)
	if maxItems > 0 && n > maxItems {
		n = maxItems
	}
	base := "[" + strings.Join(ss[:n], ",") + "]"
	if maxItems > 0 && len(ss) > n {
		base += fmt.Sprintf("+%d_more", len(ss)-n)
	}
	return truncateLog(base, logMaxChars)
}

// formatOrderMessageToDriver builds the text sent to the driver for a new order (distance + client phone if available).
func formatOrderMessageToDriver(distKm float64, riderPhone string) string {
	text := fmt.Sprintf("Янги сўров (%.1f км узоқда).", distKm)
	if riderPhone != "" {
		text += "\n📞 Мижоз: " + riderPhone
	}
	text += "\nҚабул қиласизми?"
	return text
}

// parseUTCTime parses "2006-01-02 15:04:05" as UTC (stored timestamps are UTC).
func parseUTCTime(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
}

// isDriverSharingLiveLocation returns true only when last_live_location_at is within 90s (Telegram live updates only).
func (s *MatchService) isDriverSharingLiveLocation(ctx context.Context, driverUserID int64) bool {
	var lastLive sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT last_live_location_at FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&lastLive); err != nil || !lastLive.Valid || lastLive.String == "" {
		return false
	}
	t, err := parseUTCTime(lastLive.String)
	if err != nil {
		return false
	}
	cutoff := time.Now().UTC().Add(-time.Duration(liveLocationActiveSeconds) * time.Second)
	return t.After(cutoff)
}

// MatchService handles ride request dispatch: batches of nearest drivers, 10s acceptance timeout per batch, then next batch.
type MatchService struct {
	db                 *sql.DB
	bot                *tgbotapi.BotAPI
	cfg                *config.Config
	lastDriverNotif    map[int64]time.Time
	lastDriverNotifMu  sync.Mutex
}

// NewMatchService returns a MatchService that sends request messages via the driver bot.
func NewMatchService(db *sql.DB, driverBot *tgbotapi.BotAPI, cfg *config.Config) *MatchService {
	return &MatchService{db: db, bot: driverBot, cfg: cfg, lastDriverNotif: make(map[int64]time.Time)}
}

// insertOfferNotification stores a SENT row for GET /driver/available-requests polling.
// chat_id/message_id 0 means no Telegram message (assignment_service skips delete when message_id is 0).
func (s *MatchService) insertOfferNotification(ctx context.Context, requestID string, driverUserID, chatID int64, messageID int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO request_notifications (request_id, driver_user_id, chat_id, message_id, status, created_at)
		VALUES (?1, ?2, ?3, ?4, ?5, datetime('now'))
		ON CONFLICT(request_id, driver_user_id) DO UPDATE SET
			chat_id = excluded.chat_id,
			message_id = excluded.message_id,
			status = excluded.status,
			created_at = excluded.created_at`,
		requestID, driverUserID, chatID, messageID, domain.NotificationStatusSent)
	return err
}

// AdminOfferRequestToDriver sends a single offer for a request to a specific driver (admin dashboard Live Map).
// It does NOT auto-assign; it only notifies the driver and records a SENT row in request_notifications.
func (s *MatchService) AdminOfferRequestToDriver(ctx context.Context, requestID string, driverUserID int64) error {
	if s == nil || s.db == nil {
		return ErrAdminOfferServiceUnavailable
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || driverUserID <= 0 {
		return fmt.Errorf("invalid request_id or driver_id")
	}

	// Ensure request exists and is dispatchable (PENDING + TTL).
	var pickupLat, pickupLng float64
	var st string
	err := s.db.QueryRowContext(ctx, `SELECT pickup_lat, pickup_lng, status FROM ride_requests WHERE id = ?1`, requestID).
		Scan(&pickupLat, &pickupLng, &st)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAdminOfferUnknownRequest
		}
		return err
	}
	if st != domain.RequestStatusPending || !s.requestStillDispatchable(ctx, requestID) {
		return ErrAdminOfferRequestNotAvail
	}
	var riderPhone string
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(u.phone,'') FROM ride_requests r JOIN users u ON u.id = r.rider_user_id WHERE r.id = ?1`, requestID).Scan(&riderPhone)
	riderPhone = strings.TrimSpace(riderPhone)

	// Load driver + enforce base eligibility gates (approved + legal + online/not busy).
	var telegramID int64
	var lat, lng float64
	var isActive int
	var manualOffline int
	var lastSeenAt sql.NullString
	balanceCond := " AND d.balance > 0"
	if s.cfg != nil && s.cfg.InfiniteDriverBalance {
		balanceCond = ""
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT u.telegram_id, COALESCE(d.last_lat, 0), COALESCE(d.last_lng, 0), COALESCE(d.is_active, 0), COALESCE(d.manual_offline, 0), d.last_seen_at
		FROM drivers d JOIN users u ON u.id = d.user_id
		WHERE d.user_id = ?1 AND d.verification_status = 'approved' AND `+legal.SQLDriverDispatchLegalOK+``+balanceCond,
		driverUserID).Scan(&telegramID, &lat, &lng, &isActive, &manualOffline, &lastSeenAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAdminOfferUnknownDriver
		}
		return err
	}
	if isActive != 1 || manualOffline == 1 {
		return ErrAdminOfferDriverNotElig
	}
	if !lastSeenAt.Valid || lastSeenAt.String == "" {
		return ErrAdminOfferDriverNotElig
	}
	if t, perr := parseUTCTime(lastSeenAt.String); perr == nil {
		if time.Since(t) > driverLocationFreshnessSeconds*time.Second {
			return ErrAdminOfferDriverNotElig
		}
	} else {
		return ErrAdminOfferDriverNotElig
	}
	// Driver must not already have an active trip.
	var activeTripID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT id FROM trips WHERE driver_user_id = ?1 AND status IN ('WAITING','ARRIVED','STARTED') LIMIT 1`,
		driverUserID).Scan(&activeTripID)
	if activeTripID != "" {
		return ErrAdminOfferDriverNotElig
	}

	distKm := utils.HaversineMeters(pickupLat, pickupLng, lat, lng) / 1000

	text := formatOrderMessageToDriver(distKm, riderPhone)
	if !s.isDriverSharingLiveLocation(ctx, driverUserID) {
		text += liveLocationOrderHint
	}
	chatID, msgID := int64(0), 0
	if s.bot != nil && telegramID != 0 {
		msg := tgbotapi.NewMessage(telegramID, text)
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Қабул қилиш", acceptCallbackPrefix+requestID),
			),
		)
		sentMsg, sendErr := s.bot.Send(msg)
		if sendErr != nil {
			log.Printf("match_service: admin offer send to driver=%d: %v", telegramID, truncateLog(sendErr.Error(), logMaxChars))
		} else {
			chatID = telegramID
			msgID = sentMsg.MessageID
		}
	}
	_ = s.insertOfferNotification(ctx, requestID, driverUserID, chatID, msgID)
	return nil
}

// driverCandidate is an eligible driver with distance for sorting.
type driverCandidate struct {
	UserID     int64
	TelegramID int64
	LastLat    float64
	LastLng    float64
	DistKm     float64
}

// StartPriorityDispatch starts a goroutine: notify closest driver, wait 8s, if no response notify next.
func (s *MatchService) StartPriorityDispatch(ctx context.Context, requestID string) {
	go s.runPriorityDispatch(ctx, requestID)
}

// requestStillDispatchable returns true if the request is PENDING and not past TTL (expires_at > now).
func (s *MatchService) requestStillDispatchable(ctx context.Context, requestID string) bool {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM ride_requests WHERE id = ?1 AND status = ?2 AND expires_at > datetime('now')`,
		requestID, domain.RequestStatusPending).Scan(&count)
	return err == nil && count == 1
}

func (s *MatchService) runPriorityDispatch(ctx context.Context, requestID string) {
	var pickupLat, pickupLng, radiusKm float64
	var status string
	err := s.db.QueryRowContext(ctx, `
		SELECT pickup_lat, pickup_lng, radius_km, status FROM ride_requests WHERE id = ?1`,
		requestID).Scan(&pickupLat, &pickupLng, &radiusKm, &status)
	if err != nil || status != domain.RequestStatusPending {
		return
	}
	var riderPhone string
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(u.phone,'') FROM ride_requests r JOIN users u ON u.id = r.rider_user_id WHERE r.id = ?1`, requestID).Scan(&riderPhone)
	riderPhone = strings.TrimSpace(riderPhone)
	// Request TTL: do not dispatch if already expired
	if !s.requestStillDispatchable(ctx, requestID) {
		log.Printf("dispatch_audit: request=%s skipped (expired or not PENDING)", requestID)
		return
	}
	// Grid prefilter: only look at drivers whose grid is in the 3x3 neighborhood of the pickup.
	gridIDs := utils.NeighborGridIDs(pickupLat, pickupLng)
	if s.cfg != nil && s.cfg.DispatchDebug {
		log.Printf("dispatch_debug: request=%s pickup=(%.5f,%.5f) grids_count=%d grids=%v", requestID, pickupLat, pickupLng, len(gridIDs), gridIDs)
	}
	// Dispatch only for drivers with Telegram Live Location: live_location_active=1 and last_live_location_at within 90s.
	locationFreshSinceStr := time.Now().Add(-time.Duration(driverLocationFreshnessSeconds) * time.Second).UTC().Format("2006-01-02 15:04:05")
	// When InfiniteDriverBalance is true, all drivers get orders regardless of balance; otherwise require balance > 0.
	balanceCond := ""
	if s.cfg == nil || !s.cfg.InfiniteDriverBalance {
		balanceCond = " AND d.balance > 0"
	}
	placeholders := "?"
	args := []interface{}{locationFreshSinceStr, locationFreshSinceStr}
	for i := 1; i < len(gridIDs); i++ {
		placeholders += ",?"
	}
	for _, g := range gridIDs {
		args = append(args, g)
	}
	// With HTTP live location, Flutter/native drivers may not have full profile rows yet; omit strict
	// phone/car/plate filters so dispatch can still create request_notifications (polling + Telegram send).
	dispatchProfileCond := `
		  AND d.phone IS NOT NULL AND d.phone != ''
		  AND d.car_type IS NOT NULL AND d.car_type != ''
		  AND d.color IS NOT NULL AND d.color != ''
		  AND d.plate IS NOT NULL AND d.plate != ''`
	if s.cfg != nil && s.cfg.EnableDriverHTTPLiveLocation {
		dispatchProfileCond = ""
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.user_id, u.telegram_id, d.last_lat, d.last_lng
		FROM drivers d JOIN users u ON u.id = d.user_id
		WHERE COALESCE(d.live_location_active, 0) = 1`+balanceCond+`
		  AND d.verification_status = 'approved'
		  AND `+legal.SQLDriverDispatchLegalOK+`
		  AND d.last_live_location_at IS NOT NULL AND d.last_live_location_at >= ?2
		  AND d.last_seen_at IS NOT NULL AND d.last_seen_at >= ?1
		  AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL`+dispatchProfileCond+`
		  AND (d.grid_id IN (`+placeholders+`) OR d.grid_id IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM trips t WHERE t.driver_user_id = d.user_id AND t.status IN ('WAITING','ARRIVED','STARTED'))`,
		args...)
	if err != nil {
		rows, _ = s.db.QueryContext(ctx, `
			SELECT d.user_id, u.telegram_id, d.last_lat, d.last_lng
			FROM drivers d JOIN users u ON u.id = d.user_id
			WHERE COALESCE(d.live_location_active, 0) = 1`+balanceCond+`
			  AND d.verification_status = 'approved'
			  AND `+legal.SQLDriverDispatchLegalOK+`
			  AND d.last_live_location_at IS NOT NULL AND d.last_live_location_at >= ?2
			  AND d.last_seen_at IS NOT NULL AND d.last_seen_at >= ?1
			  AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL
			  AND (d.grid_id IN (`+placeholders+`) OR d.grid_id IS NULL)
			  AND NOT EXISTS (SELECT 1 FROM trips t WHERE t.driver_user_id = d.user_id AND t.status IN ('WAITING','ARRIVED','STARTED'))`,
			args...)
	}
	if err != nil {
		log.Printf("match_service: dispatch query: %v", truncateLog(err.Error(), logMaxChars))
		return
	}
	defer rows.Close()
	var candidates []driverCandidate
	for rows.Next() {
		var uID int64
		var telegramID int64
		var lat, lng float64
		if err := rows.Scan(&uID, &telegramID, &lat, &lng); err != nil {
			continue
		}
		distKm := utils.HaversineMeters(pickupLat, pickupLng, lat, lng) / 1000
		if distKm > radiusKm {
			continue
		}
		candidates = append(candidates, driverCandidate{UserID: uID, TelegramID: telegramID, LastLat: lat, LastLng: lng, DistKm: distKm})
	}
	// Audit: candidate drivers (always log for dispatch audit)
	{
		ids := make([]int64, 0, len(candidates))
		for _, c := range candidates {
			ids = append(ids, c.UserID)
		}
		log.Printf("dispatch_audit: request=%s candidate_drivers=%d driver_ids_sample=%s", requestID, len(candidates), sampleInt64(ids, logSliceMaxItems))
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: request=%s candidate_drivers=%d ids_sample=%s", requestID, len(candidates), sampleInt64(ids, logSliceMaxItems))
		}
	}
	if len(candidates) == 0 {
		log.Printf("match_service: no eligible drivers for request %s (live location + balance%s, within radius)", requestID, balanceCond)
		return
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].DistKm < candidates[j].DistKm })
	cooldownSec := defaultDriverCooldown
	if s.cfg != nil && s.cfg.DispatchDriverCooldownSec > 0 {
		cooldownSec = s.cfg.DispatchDriverCooldownSec
	}
	batchWaitSec := dispatchBatchWaitSec
	if s.cfg != nil && s.cfg.DispatchWaitSeconds > 0 {
		batchWaitSec = s.cfg.DispatchWaitSeconds
	}

	// Process drivers in batches of N; wait 10s per batch for acceptance; if no accept, send to next batch.
	for batchStart := 0; batchStart < len(candidates); batchStart += dispatchBatchSize {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !s.requestStillDispatchable(ctx, requestID) {
			log.Printf("dispatch_audit: request=%s stopped (expired or no longer PENDING)", requestID)
			return
		}
		batchEnd := batchStart + dispatchBatchSize
		if batchEnd > len(candidates) {
			batchEnd = len(candidates)
		}
		batch := candidates[batchStart:batchEnd]
		var batchDriverIDs []int64

		for _, c := range batch {
			var currentStatus string
			if err := s.db.QueryRowContext(ctx, `SELECT status FROM ride_requests WHERE id = ?1`, requestID).Scan(&currentStatus); err != nil || currentStatus != domain.RequestStatusPending {
				return
			}
			var exists int
			if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM request_notifications WHERE request_id = ?1 AND driver_user_id = ?2`, requestID, c.UserID).Scan(&exists); err == nil {
				continue
			}
			s.lastDriverNotifMu.Lock()
			last, ok := s.lastDriverNotif[c.UserID]
			if ok && time.Since(last) < time.Duration(cooldownSec)*time.Second {
				s.lastDriverNotifMu.Unlock()
				continue
			}
			s.lastDriverNotif[c.UserID] = time.Now()
			s.lastDriverNotifMu.Unlock()
			log.Printf("dispatch_audit: request=%s batch_send driver=%d dist_km=%.3f", requestID, c.UserID, c.DistKm)
			if s.cfg != nil && s.cfg.DispatchDebug {
				log.Printf("dispatch_debug: request=%s try_driver=%d dist_km=%.3f", requestID, c.UserID, c.DistKm)
			}
			text := formatOrderMessageToDriver(c.DistKm, riderPhone)
			if !s.isDriverSharingLiveLocation(ctx, c.UserID) {
				text += liveLocationOrderHint
			}
			msg := tgbotapi.NewMessage(c.TelegramID, text)
			msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("✅ Қабул қилиш", acceptCallbackPrefix+requestID),
				),
			)
			sentMsg, sendErr := s.bot.Send(msg)
			chatID, msgID := c.TelegramID, 0
			if sendErr != nil {
				log.Printf("match_service: send to driver %d: %v", c.TelegramID, truncateLog(sendErr.Error(), logMaxChars))
				// Standalone / web drivers poll GET /driver/available-requests; rows are only created after Send today.
				// When ENABLE_DRIVER_HTTP_LIVE_LOCATION is on, still record the offer so native clients see it.
				if s.cfg == nil || !s.cfg.EnableDriverHTTPLiveLocation {
					continue
				}
				chatID = 0
				msgID = 0
			} else {
				msgID = sentMsg.MessageID
			}
			if err := s.insertOfferNotification(ctx, requestID, c.UserID, chatID, msgID); err != nil {
				log.Printf("match_service: insert request_notifications request=%s driver=%d: %v", requestID, c.UserID, truncateLog(err.Error(), logMaxChars))
				continue
			}
			batchDriverIDs = append(batchDriverIDs, c.UserID)
		}

		// Wait batchWaitSec for any driver in this batch to accept; poll every second.
		for wait := 0; wait < batchWaitSec; wait++ {
			time.Sleep(1 * time.Second)
			var st string
			if err := s.db.QueryRowContext(ctx, `SELECT status FROM ride_requests WHERE id = ?1`, requestID).Scan(&st); err != nil {
				return
			}
			if st != domain.RequestStatusPending {
				return // accepted or cancelled/expired
			}
		}

		// Nobody in this batch accepted; mark their notifications as timeout and continue to next batch.
		for _, driverID := range batchDriverIDs {
			_, _ = s.db.ExecContext(ctx, `UPDATE request_notifications SET status = ?1 WHERE request_id = ?2 AND driver_user_id = ?3`,
				domain.NotificationStatusTimeout, requestID, driverID)
		}
		log.Printf("dispatch_audit: request=%s batch_timeout drivers_count=%d drivers_sample=%s after=%ds next_batch", requestID, len(batchDriverIDs), sampleInt64(batchDriverIDs, logSliceMaxItems), batchWaitSec)
	}
}

// BroadcastRequest starts batched priority dispatch (nearest first, 10s per batch, then next batch). Used by rider and radius expansion.
func (s *MatchService) BroadcastRequest(ctx context.Context, requestID string) error {
	s.StartPriorityDispatch(ctx, requestID)
	return nil
}

// PulseDriverOnlineFromHTTP marks an eligible driver online and triggers NotifyDriverOfPendingRequests when
// ENABLE_DRIVER_HTTP_LIVE_LOCATION is used with POST /driver/location. Only called from that HTTP path (never from Telegram bots).
// Eligibility matches NotifyDriverOfPendingRequests (approved, legal, lat/lng, balance) — not the stricter grid-dispatch
// profile (phone/car/plate), so Flutter/native clients still get request_notifications + polling without filling every profile field.
// Broadcast dispatch for new rider requests still uses the full candidate rules in runPriorityDispatch.
func (s *MatchService) PulseDriverOnlineFromHTTP(ctx context.Context, driverUserID int64) {
	if s == nil || s.db == nil {
		return
	}
	var activeTrip string
	_ = s.db.QueryRowContext(ctx, `
		SELECT id FROM trips WHERE driver_user_id = ?1 AND status IN ('WAITING','ARRIVED','STARTED') LIMIT 1`,
		driverUserID).Scan(&activeTrip)
	if activeTrip != "" {
		return
	}
	balanceCond := " AND d.balance > 0"
	if s.cfg != nil && s.cfg.InfiniteDriverBalance {
		balanceCond = ""
	}
	var uid int64
	err := s.db.QueryRowContext(ctx, `
		SELECT d.user_id FROM drivers d
		WHERE d.user_id = ?1 AND d.verification_status = 'approved' AND `+legal.SQLDriverDispatchLegalOK+`
		AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL`+balanceCond,
		driverUserID).Scan(&uid)
	if err != nil {
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: pulse_http driver=%d skipped (eligibility): %v", driverUserID, truncateLog(err.Error(), logMaxChars))
		}
		return
	}
	nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = s.db.ExecContext(ctx, `UPDATE drivers SET is_active = 1, manual_offline = 0, last_seen_at = ?1 WHERE user_id = ?2`, nowStr, driverUserID)
	go s.NotifyDriverOfPendingRequests(context.Background(), driverUserID)
}

// NotifyDriverOfPendingRequests sends any PENDING ride requests (within this driver's radius) to a driver who just came online.
// Skips requests already sent to this driver (request_notifications). Does not change existing dispatch logic.
// A short delay allows a nearly-simultaneous rider request to be committed so the driver receives it.
// Only sends if live_location_active and last_live_location_at are fresh (same window as dispatch).
func (s *MatchService) NotifyDriverOfPendingRequests(ctx context.Context, driverUserID int64) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(800 * time.Millisecond):
	}
	var telegramID int64
	var lat, lng float64
	var isActive int
	var lastSeenAt, lastLiveAt sql.NullString
	var liveLocationActive int
	balanceCond := " AND d.balance > 0"
	if s.cfg != nil && s.cfg.InfiniteDriverBalance {
		balanceCond = ""
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT u.telegram_id, d.last_lat, d.last_lng, d.is_active, d.last_seen_at, d.last_live_location_at, COALESCE(d.live_location_active, 0)
		FROM drivers d JOIN users u ON u.id = d.user_id
		WHERE d.user_id = ?1 AND d.verification_status = 'approved' AND `+legal.SQLDriverDispatchLegalOK+` AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL`+balanceCond,
		driverUserID).Scan(&telegramID, &lat, &lng, &isActive, &lastSeenAt, &lastLiveAt, &liveLocationActive)
	if err != nil {
		return
	}
	if isActive != 1 {
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: driver=%d skipped: is_active=%d", driverUserID, isActive)
		}
		return
	}
	// Dispatch only for Telegram Live Location: live_location_active=1 and last_live_location_at within 90s.
	if liveLocationActive != 1 {
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: driver=%d skipped: live_location_active=%d", driverUserID, liveLocationActive)
		}
		return
	}
	if !lastLiveAt.Valid || lastLiveAt.String == "" {
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: driver=%d skipped: no last_live_location_at", driverUserID)
		}
		return
	}
	if t, err := parseUTCTime(lastLiveAt.String); err == nil {
		if time.Since(t) > driverLocationFreshnessSeconds*time.Second {
			if s.cfg != nil && s.cfg.DispatchDebug {
				log.Printf("dispatch_debug: driver=%d skipped: live location stale (last_live_location_at > %ds)", driverUserID, driverLocationFreshnessSeconds)
			}
			return
		}
	} else {
		return
	}
	// Skip if driver already has an active (WAITING/ARRIVED/STARTED) trip.
	var activeTripID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT id FROM trips WHERE driver_user_id = ?1 AND status IN ('WAITING','ARRIVED','STARTED') LIMIT 1`,
		driverUserID).Scan(&activeTripID)
	if activeTripID != "" {
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: driver=%d skipped: has_active_trip=%s", driverUserID, activeTripID)
		}
		return
	}
	// Limit how far back we scan (bounded). Must cover the full configured request TTL so a
	// driver who goes live late still gets backfilled offers. Capping at 600s when
	// RequestExpiresSeconds > 3600 incorrectly skipped still-valid PENDING requests.
	lookbackSec := 600
	if s.cfg != nil && s.cfg.RequestExpiresSeconds > 0 {
		lookbackSec = s.cfg.RequestExpiresSeconds
	}
	const notifyLookbackMaxSec = 86400
	if lookbackSec > notifyLookbackMaxSec {
		lookbackSec = notifyLookbackMaxSec
	}
	cutoff := time.Now().Add(-time.Duration(lookbackSec) * time.Second).UTC().Format("2006-01-02 15:04:05")
	args := []interface{}{domain.RequestStatusPending, cutoff}
	args = append(args, driverUserID)
	// Only send requests that are still within TTL (expires_at > now)
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.pickup_lat, r.pickup_lng, r.radius_km, r.pickup_grid
		FROM ride_requests r
		WHERE r.status = ?
		  AND r.created_at >= ?
		  AND r.expires_at > datetime('now')
		  AND NOT EXISTS (SELECT 1 FROM request_notifications n WHERE n.request_id = r.id AND n.driver_user_id = ?)`,
		args...)
	if err != nil {
		log.Printf("match_service: NotifyDriverOfPendingRequests query: %v", truncateLog(err.Error(), logMaxChars))
		return
	}
	defer rows.Close()
	var toSend []struct {
		requestID string
		distKm    float64
	}
	for rows.Next() {
		var requestID string
		var pickupLat, pickupLng, radiusKm float64
		var pickupGrid sql.NullString
		if err := rows.Scan(&requestID, &pickupLat, &pickupLng, &radiusKm, &pickupGrid); err != nil {
			continue
		}
		distKm := utils.HaversineMeters(pickupLat, pickupLng, lat, lng) / 1000
		if distKm > radiusKm {
			if s.cfg != nil && s.cfg.DispatchDebug {
				log.Printf("dispatch_debug: driver=%d request=%s pickup_grid=%s dist_km=%.3f reason=outside_radius",
					driverUserID, requestID, pickupGrid.String, distKm)
			}
			continue
		}
		toSend = append(toSend, struct {
			requestID string
			distKm    float64
		}{requestID, distKm})
	}
	if s.cfg != nil && s.cfg.DispatchDebug {
		reqIDs := make([]string, 0, len(toSend))
		for _, it := range toSend {
			reqIDs = append(reqIDs, it.requestID)
		}
		log.Printf("dispatch_debug: driver=%d candidate_requests=%d req_ids_sample=%s", driverUserID, len(toSend), sampleString(reqIDs, logSliceMaxItems))
	}
	if err := rows.Err(); err != nil {
		return
	}
	for _, item := range toSend {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var status string
		if err := s.db.QueryRowContext(ctx, `SELECT status FROM ride_requests WHERE id = ?1`, item.requestID).Scan(&status); err != nil || status != domain.RequestStatusPending {
			if s.cfg != nil && s.cfg.DispatchDebug {
				log.Printf("dispatch_debug: driver=%d request=%s skipped status=%s", driverUserID, item.requestID, status)
			}
			continue
		}
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: driver=%d notify_request=%s dist_km=%.3f", driverUserID, item.requestID, item.distKm)
		}
		cooldownSec := defaultDriverCooldown
		if s.cfg != nil && s.cfg.DispatchDriverCooldownSec > 0 {
			cooldownSec = s.cfg.DispatchDriverCooldownSec
		}
		s.lastDriverNotifMu.Lock()
		last, ok := s.lastDriverNotif[driverUserID]
		if ok && time.Since(last) < time.Duration(cooldownSec)*time.Second {
			s.lastDriverNotifMu.Unlock()
			continue
		}
		s.lastDriverNotif[driverUserID] = time.Now()
		s.lastDriverNotifMu.Unlock()
		var riderPhone string
		_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(u.phone,'') FROM ride_requests r JOIN users u ON u.id = r.rider_user_id WHERE r.id = ?1`, item.requestID).Scan(&riderPhone)
		riderPhone = strings.TrimSpace(riderPhone)
		text := formatOrderMessageToDriver(item.distKm, riderPhone)
		if !s.isDriverSharingLiveLocation(ctx, driverUserID) {
			text += liveLocationOrderHint
		}
		msg := tgbotapi.NewMessage(telegramID, text)
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Қабул қилиш", acceptCallbackPrefix+item.requestID),
			),
		)
		sentMsg, sendErr := s.bot.Send(msg)
		chatID, msgID := telegramID, 0
		if sendErr != nil {
			log.Printf("match_service: send pending request to driver %d: %v", driverUserID, truncateLog(sendErr.Error(), logMaxChars))
			if s.cfg == nil || !s.cfg.EnableDriverHTTPLiveLocation {
				continue
			}
			chatID = 0
			msgID = 0
		} else {
			msgID = sentMsg.MessageID
		}
		if err := s.insertOfferNotification(ctx, item.requestID, driverUserID, chatID, msgID); err != nil {
			log.Printf("match_service: insert pending notification request=%s driver=%d: %v", item.requestID, driverUserID, truncateLog(err.Error(), logMaxChars))
			continue
		}
		time.Sleep(1 * time.Second)
	}
}

// AdminNearestDispatchDriver is one driver eligible for priority dispatch to this request, with distance from pickup.
type AdminNearestDispatchDriver struct {
	ID         int64   `json:"id"`
	TelegramID int64   `json:"telegram_id"`
	DistanceKm float64 `json:"distance_km"`
	LastLat    float64 `json:"last_lat"`
	LastLng    float64 `json:"last_lng"`
}

// AdminNearestDispatchDrivers returns drivers that are eligible to receive an admin offer for a request,
// sorted by distance ascending. Distance is computed for display/sorting only; no distance filtering is
// applied unless maxDistanceKm is explicitly provided.
func (s *MatchService) AdminNearestDispatchDrivers(ctx context.Context, requestID string, maxDistanceKm *float64) ([]AdminNearestDispatchDriver, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("match service unavailable")
	}
	var pickupLat, pickupLng float64
	err := s.db.QueryRowContext(ctx, `
		SELECT pickup_lat, pickup_lng FROM ride_requests WHERE id = ?1`, requestID).Scan(&pickupLat, &pickupLng)
	if err != nil {
		return nil, err
	}
	locationFreshSinceStr := time.Now().Add(-time.Duration(driverLocationFreshnessSeconds) * time.Second).UTC().Format("2006-01-02 15:04:05")
	balanceCond := ""
	if s.cfg == nil || !s.cfg.InfiniteDriverBalance {
		balanceCond = " AND d.balance > 0"
	}
	args := []interface{}{locationFreshSinceStr, locationFreshSinceStr}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.user_id, u.telegram_id, d.last_lat, d.last_lng
		FROM drivers d JOIN users u ON u.id = d.user_id
		WHERE COALESCE(d.live_location_active, 0) = 1`+balanceCond+`
		  AND COALESCE(d.is_active, 0) = 1
		  AND COALESCE(d.manual_offline, 0) = 0
		  AND d.verification_status = 'approved'
		  AND `+legal.SQLDriverDispatchLegalOK+`
		  AND d.last_live_location_at IS NOT NULL AND d.last_live_location_at >= ?2
		  AND d.last_seen_at IS NOT NULL AND d.last_seen_at >= ?1
		  AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM trips t WHERE t.driver_user_id = d.user_id AND t.status IN ('WAITING','ARRIVED','STARTED'))`,
		args...)
	if err != nil {
		rows, err = s.db.QueryContext(ctx, `
			SELECT d.user_id, u.telegram_id, d.last_lat, d.last_lng
			FROM drivers d JOIN users u ON u.id = d.user_id
			WHERE COALESCE(d.live_location_active, 0) = 1`+balanceCond+`
			  AND COALESCE(d.is_active, 0) = 1
			  AND COALESCE(d.manual_offline, 0) = 0
			  AND d.verification_status = 'approved'
			  AND `+legal.SQLDriverDispatchLegalOK+`
			  AND d.last_live_location_at IS NOT NULL AND d.last_live_location_at >= ?2
			  AND d.last_seen_at IS NOT NULL AND d.last_seen_at >= ?1
			  AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM trips t WHERE t.driver_user_id = d.user_id AND t.status IN ('WAITING','ARRIVED','STARTED'))`,
			args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []driverCandidate
	for rows.Next() {
		var uID int64
		var telegramID int64
		var lat, lng float64
		if err := rows.Scan(&uID, &telegramID, &lat, &lng); err != nil {
			continue
		}
		distKm := utils.HaversineMeters(pickupLat, pickupLng, lat, lng) / 1000
		if maxDistanceKm != nil && !math.IsInf(*maxDistanceKm, 0) && distKm > *maxDistanceKm {
			continue
		}
		candidates = append(candidates, driverCandidate{UserID: uID, TelegramID: telegramID, LastLat: lat, LastLng: lng, DistKm: distKm})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].DistKm < candidates[j].DistKm })
	out := make([]AdminNearestDispatchDriver, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, AdminNearestDispatchDriver{
			ID:         c.UserID,
			TelegramID: c.TelegramID,
			DistanceKm: c.DistKm,
			LastLat:    c.LastLat,
			LastLng:    c.LastLng,
		})
	}
	return out, nil
}
