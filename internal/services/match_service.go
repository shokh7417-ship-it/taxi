package services

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/utils"
)

const (
	acceptCallbackPrefix   = "accept:"
	defaultDriverCooldown  = 5
	dispatchBatchSize      = 3  // send request to N nearest drivers per batch
	dispatchBatchWaitSec   = 60 // wait this many seconds for any driver in the batch to accept before trying next batch
	liveLocationOrderHint  = "\n\n📍 Jonli lokatsiya yoqilgan bo'lsa buyurtmalar tezroq keladi."
	// Live location considered active only when last_live_location_at within 90s (same as dispatch).
	liveLocationActiveSeconds = 90
	// DriverLocationFreshnessSeconds: only drivers with last_seen_at within this many seconds are eligible for dispatch.
	driverLocationFreshnessSeconds = 90
)

// formatOrderMessageToDriver builds the text sent to the driver for a new order (distance + client phone if available).
func formatOrderMessageToDriver(distKm float64, riderPhone string) string {
	text := fmt.Sprintf("Yangi so'rov (%.1f km uzoqda).", distKm)
	if riderPhone != "" {
		text += "\n📞 Mijoz: " + riderPhone
	}
	text += "\nQabul qilasizmi?"
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
		log.Printf("dispatch_debug: request=%s pickup=(%.5f,%.5f) grids=%v", requestID, pickupLat, pickupLng, gridIDs)
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.user_id, u.telegram_id, d.last_lat, d.last_lng
		FROM drivers d JOIN users u ON u.id = d.user_id
		WHERE d.is_active = 1`+balanceCond+`
		  AND COALESCE(d.live_location_active, 0) = 1
		  AND d.verification_status = 'approved'
		  AND COALESCE(d.terms_accepted, 0) = 1
		  AND d.last_live_location_at IS NOT NULL AND d.last_live_location_at >= ?2
		  AND d.last_seen_at IS NOT NULL AND d.last_seen_at >= ?1
		  AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL
		  AND d.phone IS NOT NULL AND d.phone != ''
		  AND d.car_type IS NOT NULL AND d.car_type != ''
		  AND d.color IS NOT NULL AND d.color != ''
		  AND d.plate IS NOT NULL AND d.plate != ''
		  AND (d.grid_id IN (`+placeholders+`) OR d.grid_id IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM trips t WHERE t.driver_user_id = d.user_id AND t.status IN ('WAITING','STARTED'))`,
		args...)
	if err != nil {
		rows, _ = s.db.QueryContext(ctx, `
			SELECT d.user_id, u.telegram_id, d.last_lat, d.last_lng
			FROM drivers d JOIN users u ON u.id = d.user_id
			WHERE d.is_active = 1`+balanceCond+`
			  AND COALESCE(d.live_location_active, 0) = 1
			  AND d.verification_status = 'approved'
			  AND COALESCE(d.terms_accepted, 0) = 1
			  AND d.last_live_location_at IS NOT NULL AND d.last_live_location_at >= ?2
			  AND d.last_seen_at IS NOT NULL AND d.last_seen_at >= ?1
			  AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL
			  AND (d.grid_id IN (`+placeholders+`) OR d.grid_id IS NULL)
			  AND NOT EXISTS (SELECT 1 FROM trips t WHERE t.driver_user_id = d.user_id AND t.status IN ('WAITING','STARTED'))`,
			args...)
	}
	if err != nil {
		log.Printf("match_service: dispatch query: %v", err)
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
		log.Printf("dispatch_audit: request=%s candidate_drivers=%d driver_ids=%v", requestID, len(candidates), ids)
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: request=%s candidate_drivers=%d ids=%v", requestID, len(candidates), ids)
		}
	}
	if len(candidates) == 0 {
		log.Printf("match_service: no eligible drivers for request %s (is_active=1%s, within radius)", requestID, balanceCond)
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
					tgbotapi.NewInlineKeyboardButtonData("✅ Qabul qilish", acceptCallbackPrefix+requestID),
				),
			)
			sentMsg, err := s.bot.Send(msg)
			if err != nil {
				log.Printf("match_service: send to driver %d: %v", c.TelegramID, err)
				continue
			}
			_, _ = s.db.ExecContext(ctx, `
				INSERT INTO request_notifications (request_id, driver_user_id, chat_id, message_id, status)
				VALUES (?1, ?2, ?3, ?4, ?5)`,
				requestID, c.UserID, c.TelegramID, sentMsg.MessageID, domain.NotificationStatusSent)
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
		log.Printf("dispatch_audit: request=%s batch_timeout drivers=%v after=%ds next_batch", requestID, batchDriverIDs, batchWaitSec)
	}
}

// BroadcastRequest starts batched priority dispatch (nearest first, 10s per batch, then next batch). Used by rider and radius expansion.
func (s *MatchService) BroadcastRequest(ctx context.Context, requestID string) error {
	s.StartPriorityDispatch(ctx, requestID)
	return nil
}

// NotifyDriverOfPendingRequests sends any PENDING ride requests (within this driver's radius) to a driver who just came online.
// Skips requests already sent to this driver (request_notifications). Does not change existing dispatch logic.
// A short delay allows a nearly-simultaneous rider request to be committed so the driver receives it.
// Only sends if the driver's location is fresh (last_seen_at within 90s).
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
		WHERE d.user_id = ?1 AND d.verification_status = 'approved' AND COALESCE(d.terms_accepted, 0) = 1 AND d.last_lat IS NOT NULL AND d.last_lng IS NOT NULL`+balanceCond,
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
	// Skip if driver already has an active (WAITING/STARTED) trip.
	var activeTripID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT id FROM trips WHERE driver_user_id = ?1 AND status IN ('WAITING','STARTED') LIMIT 1`,
		driverUserID).Scan(&activeTripID)
	if activeTripID != "" {
		if s.cfg != nil && s.cfg.DispatchDebug {
			log.Printf("dispatch_debug: driver=%d skipped: has_active_trip=%s", driverUserID, activeTripID)
		}
		return
	}
	// Limit to recent waiting requests to avoid heavy scans.
	windowSec := s.cfg.RequestExpiresSeconds
	if windowSec <= 0 || windowSec > 3600 {
		windowSec = 600
	}
	cutoff := time.Now().Add(-time.Duration(windowSec) * time.Second).UTC().Format("2006-01-02 15:04:05")
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
		log.Printf("match_service: NotifyDriverOfPendingRequests query: %v", err)
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
		log.Printf("dispatch_debug: driver=%d candidate_requests=%d ids=%v", driverUserID, len(toSend), reqIDs)
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
				tgbotapi.NewInlineKeyboardButtonData("✅ Qabul qilish", acceptCallbackPrefix+item.requestID),
			),
		)
		sentMsg, err := s.bot.Send(msg)
		if err != nil {
			log.Printf("match_service: send pending request to driver %d: %v", driverUserID, err)
			continue
		}
		_, _ = s.db.ExecContext(ctx, `
			INSERT INTO request_notifications (request_id, driver_user_id, chat_id, message_id, status)
			VALUES (?1, ?2, ?3, ?4, ?5)`,
			item.requestID, driverUserID, telegramID, sentMsg.MessageID, domain.NotificationStatusSent)
		time.Sleep(1 * time.Second)
	}
}
