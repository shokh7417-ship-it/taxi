package services

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/logger"
	"taxi-mvp/internal/repositories"
	"taxi-mvp/internal/utils"
	"taxi-mvp/internal/ws"
)

// TripService handles trip lifecycle: start, add points, finish, cancel; notifies rider and driver.
type TripService struct {
	db                   *sql.DB
	tripRepo             *repositories.TripRepo
	riderBot             *tgbotapi.BotAPI
	driverBot            *tgbotapi.BotAPI
	cfg                  *config.Config
	hub                  HubBroadcaster
	fareSvc              *FareService // optional; if set, fare comes from DB tiered settings
	OnDriverStatusUpdate func(telegramID int64) // optional; e.g. update driver's pinned status panel after trip finish
}

// HubBroadcaster is the minimal interface for broadcasting trip events (optional; can be nil).
type HubBroadcaster interface {
	BroadcastToTrip(tripID string, event ws.Event)
}

// TripActionResult is the result of a trip state change: "updated" or "noop", and the trip status after the action.
type TripActionResult struct {
	Result string // "updated" or "noop"
	Status string // trip status after the action
}

// NewTripService returns a TripService. hub and fareSvc can be nil.
func NewTripService(db *sql.DB, tripRepo *repositories.TripRepo, riderBot, driverBot *tgbotapi.BotAPI, cfg *config.Config, hub HubBroadcaster, fareSvc *FareService) *TripService {
	if tripRepo == nil {
		tripRepo = repositories.NewTripRepo(db)
	}
	return &TripService{db: db, tripRepo: tripRepo, riderBot: riderBot, driverBot: driverBot, cfg: cfg, hub: hub, fareSvc: fareSvc}
}

// ScheduleStartReminder schedules a one-off check after StartReminderSeconds. If trip is still WAITING,
// sends a reminder to the driver. If STARTED or FINISHED, does nothing. Safe to call once per trip creation.
// For MVP uses an in-memory timer; can be replaced by DB/job later.
func (s *TripService) ScheduleStartReminder(ctx context.Context, tripID string, driverUserID int64) {
	delay := time.Duration(s.cfg.StartReminderSeconds) * time.Second
	if delay <= 0 {
		delay = 60 * time.Second
	}
	log.Printf("trip_service: start reminder scheduled for trip %s in %v", tripID, delay)
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		var status string
		err := s.db.QueryRowContext(context.Background(), `SELECT status FROM trips WHERE id = ?1 AND driver_user_id = ?2`, tripID, driverUserID).Scan(&status)
		if err != nil {
			if err != sql.ErrNoRows {
				log.Printf("trip_service: start reminder load trip: %v", err)
			}
			return
		}
		if status != domain.TripStatusWaiting {
			log.Printf("trip_service: start reminder skipped for trip %s (status=%s)", tripID, status)
			return
		}
		log.Printf("trip_service: start reminder fired for trip %s (no message sent)", tripID)
	}()
}

// StartTrip sets trip to STARTED when status is WAITING. Idempotent: if already STARTED returns noop.
// Uses state machine and conditional UPDATE for race safety.
func (s *TripService) StartTrip(ctx context.Context, tripID string, driverUserID int64) (*TripActionResult, error) {
	current, err := s.tripRepo.GetStatus(ctx, tripID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrTripNotFound
		}
		return nil, err
	}
	if current == domain.TripStatusStarted {
		logger.TripEvent("trip_start", tripID, "noop", logger.TripEventAttrs(driverUserID, 0)...)
		return &TripActionResult{Result: "noop", Status: domain.TripStatusStarted}, nil
	}
	if err := domain.ValidateTransition(current, domain.TripStatusStarted); err != nil {
		return nil, err
	}
	n, err := s.tripRepo.UpdateToStarted(ctx, tripID, driverUserID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		// Race: re-read status; if another request already started, treat as noop
		current, _ = s.tripRepo.GetStatus(ctx, tripID)
		if current == domain.TripStatusStarted {
			return &TripActionResult{Result: "noop", Status: domain.TripStatusStarted}, nil
		}
		return nil, domain.ErrInvalidTransition
	}
	var riderUserID int64
	_ = s.db.QueryRowContext(ctx, `SELECT rider_user_id FROM trips WHERE id = ?1`, tripID).Scan(&riderUserID)
	if riderUserID != 0 {
		var riderTelegramID int64
		if err := s.db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, riderUserID).Scan(&riderTelegramID); err == nil {
			// When trip starts, remove the cancel button from rider keyboard (keep only "Track driver").
			kb := tgbotapi.NewReplyKeyboard(
				tgbotapi.NewKeyboardButtonRow(
					tgbotapi.NewKeyboardButton("📍 Haydovchini kuzatish"),
				),
			)
			kb.ResizeKeyboard = true
			msg := tgbotapi.NewMessage(riderTelegramID, "Safar boshlandi ▶️")
			msg.ReplyMarkup = kb
			if _, err := s.riderBot.Send(msg); err != nil {
				log.Printf("trip_service: notify rider start: %v", err)
			}
		}
	}
	if s.hub != nil {
		baseFare := int64(0)
		if s.fareSvc != nil {
			if settings, err := s.fareSvc.GetFareSettings(ctx); err == nil {
				baseFare = settings.BaseFare
			}
		}
		if baseFare == 0 && s.cfg != nil {
			baseFare = int64(s.cfg.StartingFee)
		}
		s.hub.BroadcastToTrip(tripID, ws.Event{
			Type:       "trip_started",
			TripStatus: domain.TripStatusStarted,
			Payload: map[string]interface{}{
				"trip_status": domain.TripStatusStarted,
				"distance_m":  int64(0),
				"distance_km": 0.0,
				"fare":        baseFare,
			},
		})
	}
	logger.TripEvent("trip_start", tripID, "updated", logger.TripEventAttrs(driverUserID, riderUserID)...)
	return &TripActionResult{Result: "updated", Status: domain.TripStatusStarted}, nil
}

// MinSpeedKmh is the minimum speed (km/h) for a segment to count toward fare distance (avoids GPS noise).
const MinSpeedKmh = 2.0

// AddPoint appends a location only when trip status is STARTED. When segment is valid (movement >= 5m, speed > 2 km/h),
// adds the segment to trips.distance_m via AddTripDistance so GET /trip/:id returns live distance.
// Returns accepted (true if point was stored), ignoreReason (e.g. "trip not started", "movement too small"), and error.
func (s *TripService) AddPoint(ctx context.Context, tripID string, driverUserID int64, lat, lng float64, ts time.Time) (accepted bool, ignoreReason string, err error) {
	var status string
	err = s.db.QueryRowContext(ctx, `SELECT status FROM trips WHERE id = ?1 AND driver_user_id = ?2`, tripID, driverUserID).Scan(&status)
	if err != nil {
		if err == sql.ErrNoRows {
			logger.DriverLocation(tripID, driverUserID, "ignored", "trip not started")
			return false, "trip not started", nil
		}
		return false, "", err
	}
	if status != domain.TripStatusStarted {
		logger.DriverLocation(tripID, driverUserID, "ignored", "trip not started")
		return false, "trip not started", nil
	}
	var prevLat, prevLng float64
	var prevTs interface{}
	err = s.db.QueryRowContext(ctx, `
		SELECT lat, lng, ts FROM trip_locations
		WHERE trip_id = ?1 ORDER BY ts DESC LIMIT 1`,
		tripID).Scan(&prevLat, &prevLng, &prevTs)
	hasPrev := (err == nil)
	if hasPrev {
		movementM := utils.HaversineMeters(prevLat, prevLng, lat, lng)
		if movementM < 5 {
			logger.DriverLocation(tripID, driverUserID, "ignored", "movement too small")
			return false, "movement too small", nil
		}
	}
	// Store ts in DB-friendly format so SELECT returns parseable value (SQLite TEXT).
	tsStr := ts.UTC().Format("2006-01-02 15:04:05")
	_, err = s.db.ExecContext(ctx, `INSERT INTO trip_locations (trip_id, lat, lng, ts) VALUES (?1, ?2, ?3, ?4)`, tripID, lat, lng, tsStr)
	if err != nil {
		return false, "", err
	}
	if hasPrev {
		segmentM := utils.HaversineMeters(prevLat, prevLng, lat, lng)
		segmentKm := segmentM / 1000
		prevTime := parseTripLocationTime(prevTs)
		durationSec := ts.Sub(prevTime).Seconds()
		if durationSec <= 0 {
			durationSec = 1 // fallback: avoid dropping distance when time parse fails or clock skew
		}
		speedKmh := segmentKm * 3600 / durationSec
		if speedKmh <= MinSpeedKmh {
			logger.DriverLocation(tripID, driverUserID, "accepted", "speed too slow")
		}
		if speedKmh > MinSpeedKmh {
			addM := int64(math.Round(segmentM))
			n, upErr := s.tripRepo.AddTripDistance(ctx, tripID, addM)
			if upErr != nil {
				log.Printf("trip_service: AddPoint distance_m update failed: %v", upErr)
			} else if n == 0 {
				log.Printf("trip_service: AddPoint distance_m update affected 0 rows (trip=%s)", tripID)
			}
		}
	}
	return true, "", nil
}

// parseTripLocationTime converts a trip_locations.ts value from DB (string, []byte, int64, time.Time) to time.Time.
func parseTripLocationTime(v interface{}) time.Time {
	if v == nil {
		return time.Time{}
	}
	switch val := v.(type) {
	case time.Time:
		return val
	case string:
		if t, err := time.Parse("2006-01-02 15:04:05", val); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t
		}
		if t, err := time.Parse("2006-01-02T15:04:05", val); err == nil {
			return t
		}
	case []byte:
		return parseTripLocationTime(string(val))
	case int64:
		if val > 1e12 {
			return time.UnixMilli(val)
		}
		return time.Unix(val, 0)
	case float64:
		return time.Unix(int64(val), 0)
	}
	return time.Time{}
}

// FinishTrip sets trip to FINISHED when status is STARTED. Idempotent: if already FINISHED returns noop.
// Uses state machine and conditional UPDATE; computes fare server-side.
func (s *TripService) FinishTrip(ctx context.Context, tripID string, driverUserID int64) (*TripActionResult, error) {
	current, err := s.tripRepo.GetStatus(ctx, tripID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrTripNotFound
		}
		return nil, err
	}
	if current == domain.TripStatusFinished {
		logger.TripEvent("trip_finish", tripID, "noop", logger.TripEventAttrs(driverUserID, 0)...)
		return &TripActionResult{Result: "noop", Status: domain.TripStatusFinished}, nil
	}
	if err := domain.ValidateTransition(current, domain.TripStatusFinished); err != nil {
		return nil, err
	}
	var distanceM int64
	var riderUserID int64
	err = s.db.QueryRowContext(ctx, `SELECT distance_m, rider_user_id FROM trips WHERE id = ?1 AND driver_user_id = ?2 AND status = ?3`,
		tripID, driverUserID, domain.TripStatusStarted).Scan(&distanceM, &riderUserID)
	if err != nil {
		if err == sql.ErrNoRows {
			current, _ = s.tripRepo.GetStatus(ctx, tripID)
			if current == domain.TripStatusFinished {
				return &TripActionResult{Result: "noop", Status: domain.TripStatusFinished}, nil
			}
			return nil, domain.ErrInvalidTransition
		}
		return nil, err
	}
	var rawFare int64
	if s.fareSvc != nil {
		rawFare, _ = s.fareSvc.CalculateFare(ctx, float64(distanceM)/1000)
	}
	if s.fareSvc == nil && s.cfg != nil {
		rawFare = utils.CalculateFareRounded(float64(s.cfg.StartingFee), float64(s.cfg.PricePerKm), float64(distanceM)/1000)
	}
	// Normalized fare: if > 50 so'm round to nearest 100; if <= 50 so'm then 0. Stored and shown to users; commission taken from this.
	fareAmount := normalizeFare(rawFare)
	// Rider referral bonus: apply as fare discount (non-withdrawable); deduct from rider's referral_bonus_balance.
	var riderBonusUsed int64
	var riderBonusBalance int64
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(referral_bonus_balance, 0) FROM users WHERE id = ?1`, riderUserID).Scan(&riderBonusBalance)
	if riderBonusBalance > 0 && fareAmount > 0 {
		riderBonusUsed = fareAmount
		if riderBonusUsed > riderBonusBalance {
			riderBonusUsed = riderBonusBalance
		}
		if riderBonusUsed > 0 {
			_, _ = s.db.ExecContext(ctx, `UPDATE users SET referral_bonus_balance = referral_bonus_balance - ?1 WHERE id = ?2`, riderBonusUsed, riderUserID)
		}
	}
	n, err := s.tripRepo.UpdateToFinished(ctx, tripID, driverUserID, fareAmount, riderBonusUsed)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		current, _ = s.tripRepo.GetStatus(ctx, tripID)
		if current == domain.TripStatusFinished {
			return &TripActionResult{Result: "noop", Status: domain.TripStatusFinished}, nil
		}
		return nil, domain.ErrInvalidTransition
	}
	// Driver referral: reward (100000 so'm total) only after referred driver completes 5 trips AND is active with live location (anti-fake).
	var referredBy sql.NullString
	var stage2Paid int
	_ = s.db.QueryRowContext(ctx, `SELECT referred_by, COALESCE(referral_stage2_reward_paid, 0) FROM users WHERE id = ?1`, driverUserID).Scan(&referredBy, &stage2Paid)
	if referredBy.Valid && referredBy.String != "" && stage2Paid == 0 {
		var finishedCount int64
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trips WHERE driver_user_id = ?1 AND status = ?2`, driverUserID, domain.TripStatusFinished).Scan(&finishedCount)
		if finishedCount >= 5 {
			// Require referred driver to be active and sharing live location (within 90s).
			var isActive, liveActive int
			var lastLiveAt sql.NullString
			_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(d.is_active, 0), COALESCE(d.live_location_active, 0), d.last_live_location_at FROM drivers d WHERE d.user_id = ?1`, driverUserID).Scan(&isActive, &liveActive, &lastLiveAt)
			liveRecent := false
			if liveActive == 1 && lastLiveAt.Valid && lastLiveAt.String != "" {
				if t, err := time.ParseInLocation("2006-01-02 15:04:05", lastLiveAt.String, time.UTC); err == nil && time.Since(t) <= 90*time.Second {
					liveRecent = true
				}
			}
			if isActive == 1 && liveRecent {
				// Pay stage2 reward 100000 so'm (stage1 = 20k was paid when referred driver completed application).
				res, _ := s.db.ExecContext(ctx, `UPDATE drivers SET balance = balance + 100000 WHERE user_id = (SELECT id FROM users WHERE referral_code = ?1)`, referredBy.String)
				if nr, _ := res.RowsAffected(); nr > 0 {
					_, _ = s.db.ExecContext(ctx, `UPDATE users SET referral_stage2_reward_paid = 1 WHERE id = ?1`, driverUserID)
				}
			}
		}
	}
	// New user bonus: 80k so'm once when driver completes 5 successful trips. Pay only when COUNT(finished) >= 5 and not yet paid.
	var finishedCount int64
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trips WHERE driver_user_id = ?1 AND status = ?2`, driverUserID, domain.TripStatusFinished).Scan(&finishedCount)
	tripsPendingForBonus := 0
	if finishedCount < 5 {
		tripsPendingForBonus = 5 - int(finishedCount)
	}
	// Atomic: only add bonus when driver has 5+ finished trips AND five_trips_bonus_paid = 0.
	res, err := s.db.ExecContext(ctx, `
		UPDATE drivers SET balance = balance + 80000, five_trips_bonus_paid = 1
		WHERE user_id = ?1 AND COALESCE(five_trips_bonus_paid, 0) = 0
		  AND (SELECT COUNT(*) FROM trips WHERE driver_user_id = ?1 AND status = ?2) >= 5`,
		driverUserID, driverUserID, domain.TripStatusFinished)
	if err != nil {
		log.Printf("trip_service: five_trips_bonus update failed (driver=%d): %v", driverUserID, err)
	} else if nr, _ := res.RowsAffected(); nr > 0 && s.driverBot != nil {
		var driverTgID int64
		if err := s.db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, driverUserID).Scan(&driverTgID); err == nil && driverTgID != 0 {
			msg := tgbotapi.NewMessage(driverTgID, "🎉 Sizning 80 000 so'm mukofotingiz hisobingizga qo'shildi.")
			if _, err := s.driverBot.Send(msg); err != nil {
				log.Printf("trip_service: notify driver 80k bonus: %v", err)
			}
		}
	}
	// Always deduct commission from normalized fare and record payment (fareAmount already normalized).
	if s.cfg != nil && fareAmount > 0 {
		pc := 5
		if s.fareSvc != nil {
			if settings, err := s.fareSvc.GetFareSettings(ctx); err == nil && settings != nil && settings.CommissionPercent > 0 {
				pc = settings.CommissionPercent
			}
		}
		if pc <= 0 && s.cfg.CommissionPercent > 0 {
			pc = s.cfg.CommissionPercent
		}
		if pc <= 0 {
			pc = 5
		}
		// Commission = x% of fare price.
		commission := (fareAmount * int64(pc)) / 100
		if commission > 0 {
			// Deduct commission from driver balance.
			if _, err := s.db.ExecContext(ctx, `
				UPDATE drivers SET balance = balance - ?1 WHERE user_id = ?2`,
				commission, driverUserID); err != nil {
				log.Printf("trip_service: commission balance update failed (trip=%s, driver=%d, commission=%d): %v",
					tripID, driverUserID, commission, err)
			} else {
				// Ensure driver is inactive when balance is 0 or negative.
				if _, err := s.db.ExecContext(ctx, `
					UPDATE drivers SET is_active = CASE WHEN balance > 0 THEN is_active ELSE 0 END WHERE user_id = ?1`,
					driverUserID); err != nil {
					log.Printf("trip_service: commission is_active sync failed (driver=%d): %v", driverUserID, err)
				}
			}
			// Record commission in payments ledger (trip_id links to trip for total_price in admin API).
			if _, err := s.db.ExecContext(ctx, `
				INSERT INTO payments (driver_id, amount, type, note, trip_id)
				VALUES (?1, ?2, 'commission', 'Trip commission deduction', ?3)`,
				driverUserID, commission, tripID); err != nil {
				log.Printf("trip_service: commission payment insert failed (trip=%s, driver=%d, commission=%d): %v",
					tripID, driverUserID, commission, err)
			}
		}
	}
	summary := formatTripSummary(distanceM, fareAmount, riderBonusUsed)
	var riderTelegramID, driverTelegramID int64
	_ = s.db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, riderUserID).Scan(&riderTelegramID)
	_ = s.db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, driverUserID).Scan(&driverTelegramID)
	if riderTelegramID != 0 {
		m := tgbotapi.NewMessage(riderTelegramID, summary)
		// Restore main menu so rider is not stuck with outdated cancel-only keyboard
		riderMainMenu := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("🚕 Yangi taxi chaqirish"),
				tgbotapi.NewKeyboardButton("ℹ️ Yordam"),
			),
		)
		riderMainMenu.ResizeKeyboard = true
		m.ReplyMarkup = riderMainMenu
		if _, err := s.riderBot.Send(m); err != nil {
			log.Printf("trip_service: notify rider finish: %v", err)
		}
	}
	if driverTelegramID != 0 {
		// Trip finish: status message + distance/fare; keyboard single row [Jonli lokatsiya yoqish | Ishni boshlash].
		driverSummary := formatDriverTripCompletionMessage(distanceM, fareAmount)
		kb := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("📡 Jonli lokatsiya yoqish"),
				tgbotapi.NewKeyboardButton("🟢 Ishni boshlash"),
			),
		)
		kb.ResizeKeyboard = true
		m := tgbotapi.NewMessage(driverTelegramID, driverSummary)
		m.ReplyMarkup = kb
		if _, err := s.driverBot.Send(m); err != nil {
			log.Printf("trip_service: notify driver finish: %v", err)
		}
		// Remind how many trips left until 80k so'm bonus (only if not yet received).
		if tripsPendingForBonus > 0 && s.driverBot != nil {
			pendingMsg := fmt.Sprintf("📊 Yana %d ta muvaffaqiyatli safar — 80 000 so'm mukofot sizniki.", tripsPendingForBonus)
			if tripsPendingForBonus == 1 {
				pendingMsg = "📊 Yana 1 ta muvaffaqiyatli safar — 80 000 so'm mukofot sizniki."
			}
			if _, err := s.driverBot.Send(tgbotapi.NewMessage(driverTelegramID, pendingMsg)); err != nil {
				log.Printf("trip_service: notify driver trips pending: %v", err)
			}
		}
		// After trip finish: set driver inactive only if live location is off. If live is still on, keep them online.
		var liveActive int
		var lastLiveAt sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(live_location_active, 0), last_live_location_at FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&liveActive, &lastLiveAt); err == nil {
			liveRecent := liveActive == 1 && lastLiveAt.Valid && lastLiveAt.String != ""
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", lastLiveAt.String, time.UTC); err == nil {
				liveRecent = liveRecent && time.Since(t) <= 90*time.Second
			} else {
				liveRecent = false
			}
			if !liveRecent {
				_, _ = s.db.ExecContext(ctx, `UPDATE drivers SET is_active = 0 WHERE user_id = ?1`, driverUserID)
			}
		} else {
			_, _ = s.db.ExecContext(ctx, `UPDATE drivers SET is_active = 0 WHERE user_id = ?1`, driverUserID)
		}
		// Live-location reminder only when driver is NOT sharing live, every 3 trips, and location was not just auto-updated (e.g. mini app).
		// Run after a short delay so mini app location update can land first; then we skip reminder if last_seen_at was recently updated.
		go s.maybeSendLiveLocationHintAfterTripDelayed(driverUserID, driverTelegramID)
		if s.OnDriverStatusUpdate != nil {
			s.OnDriverStatusUpdate(driverTelegramID)
		}
	}
	if s.hub != nil {
		distanceKm := float64(distanceM) / 1000
		s.hub.BroadcastToTrip(tripID, ws.Event{
			Type:       "trip_finished",
			TripStatus: domain.TripStatusFinished,
			Payload: map[string]interface{}{
				"trip_status": domain.TripStatusFinished,
				"distance_m":  distanceM,
				"distance_km": distanceKm,
				"fare_amount": fareAmount,
				"fare":        fareAmount,
			},
		})
	}
	logger.TripEvent("trip_finish", tripID, "updated", logger.TripEventAttrs(driverUserID, riderUserID)...)
	return &TripActionResult{Result: "updated", Status: domain.TripStatusFinished}, nil
}

const liveLocationBilingualInstruction = "📎 → Геопозиция / Location → Транслировать геопозицию / Share Live Location"
const liveLocationInstructionMessage   = "📍 Jonli lokatsiyani yoqsangiz, yaqin buyurtmalar sizga tezroq keladi.\n\n" + liveLocationBilingualInstruction
const liveLocationHintCooldownHours = 8
const liveLocationActiveSecReminder  = 90 // same as dispatch: only last_live_location_at within this counts as "sharing live"

const liveLocationReminderDelayAfterTrip = 5 * time.Second

// maybeSendLiveLocationHintAfterTripDelayed runs the reminder check after a delay so that if the mini app
// sends location right after trip finish, we see the updated last_seen_at and skip the reminder.
func (s *TripService) maybeSendLiveLocationHintAfterTripDelayed(driverUserID int64, driverTelegramID int64) {
	time.Sleep(liveLocationReminderDelayAfterTrip)
	s.maybeSendLiveLocationHintAfterTrip(context.Background(), driverUserID, driverTelegramID)
}

// maybeSendLiveLocationHintAfterTrip sends the live location instruction after every completed trip,
// only if the driver is NOT sharing live (last_live_location_at within 90s), hint cooldown allows, and location was not just updated.
func (s *TripService) maybeSendLiveLocationHintAfterTrip(ctx context.Context, driverUserID int64, driverTelegramID int64) {
	var lastLive, lastHint, lastSeenAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT last_live_location_at, live_location_hint_last_sent_at, last_seen_at FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&lastLive, &lastHint, &lastSeenAt); err != nil {
		return
	}
	liveCutoff := time.Now().UTC().Add(-time.Duration(liveLocationActiveSecReminder) * time.Second)
	if lastLive.Valid && lastLive.String != "" {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", lastLive.String, time.UTC)
		if err == nil && t.After(liveCutoff) {
			return // driver is sharing live location (update within 90s), no instruction
		}
	}
	cutoff := time.Now().UTC().Add(-time.Duration(liveLocationHintCooldownHours) * time.Hour)
	if lastSeenAt.Valid && lastSeenAt.String != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", lastSeenAt.String); err == nil {
			if time.Since(t) < 15*time.Second {
				return // location was just updated (e.g. mini app after finish), skip reminder
			}
		}
	}
	if lastHint.Valid && lastHint.String != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", lastHint.String); err == nil && t.After(cutoff) {
			return // already sent recently, avoid spam
		}
	}
	// Driver is offline after trip: keyboard single row [Jonli lokatsiya yoqish | Online]
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("📡 Jonli lokatsiya yoqish"),
			tgbotapi.NewKeyboardButton("🟢 Ishni boshlash"),
		),
	)
	kb.ResizeKeyboard = true
	m := tgbotapi.NewMessage(driverTelegramID, liveLocationInstructionMessage)
	m.ReplyMarkup = kb
	if _, err := s.driverBot.Send(m); err != nil {
		log.Printf("trip_service: send live location instruction after trip: %v", err)
		return
	}
	nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = s.db.ExecContext(ctx, `UPDATE drivers SET live_location_hint_last_sent_at = ?1 WHERE user_id = ?2`, nowStr, driverUserID)
}

// normalizeFare returns display fare: if rawFare <= 50 then 0; if rawFare > 50 then round to nearest 100 so'm.
func normalizeFare(rawFare int64) int64 {
	if rawFare <= 50 {
		return 0
	}
	return (rawFare + 50) / 100 * 100
}

func formatTripSummary(distanceM, fareAmount int64, riderBonusUsed int64) string {
	km := float64(distanceM) / 1000
	if riderBonusUsed > 0 {
		toPay := fareAmount - riderBonusUsed
		if toPay < 0 {
			toPay = 0
		}
		return fmt.Sprintf("Safar tugadi.\nMasofa: %.2f km\nNarx: %d so'm\nChegirma (bonus): %d so'm\nTo'lovingiz: %d so'm", km, fareAmount, riderBonusUsed, toPay)
	}
	return fmt.Sprintf("Safar tugadi.\nMasofa: %.2f km\nNarx: %d so'm", km, fareAmount)
}

// formatDriverTripCompletionMessage returns the driver trip completion message (Mini App finish): status + live location hint + distance/fare.
func formatDriverTripCompletionMessage(distanceM, fareAmount int64) string {
	km := float64(distanceM) / 1000
	return fmt.Sprintf("✅ Safar tugadi.\n📡 Jonli lokatsiya yoqilgan bo'lsa, yaqin buyurtmalar avtomatik keladi.\n\nMasofa: %.2f km\nNarx: %d so'm", km, fareAmount)
}

// CancelByDriver sets trip to CANCELLED_BY_DRIVER when status is WAITING or STARTED. Idempotent if already CANCELLED_BY_DRIVER.
func (s *TripService) CancelByDriver(ctx context.Context, tripID string, driverUserID int64) (*TripActionResult, error) {
	current, err := s.tripRepo.GetStatus(ctx, tripID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrTripNotFound
		}
		return nil, err
	}
	if current == domain.TripStatusCancelledByDriver {
		logger.TripEvent("trip_cancel_driver", tripID, "noop", logger.TripEventAttrs(driverUserID, 0)...)
		return &TripActionResult{Result: "noop", Status: domain.TripStatusCancelledByDriver}, nil
	}
	if err := domain.ValidateTransition(current, domain.TripStatusCancelledByDriver); err != nil {
		return nil, err
	}
	n, riderUserID, err := s.tripRepo.CancelByDriver(ctx, tripID, driverUserID, nil)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		current, _ = s.tripRepo.GetStatus(ctx, tripID)
		if current == domain.TripStatusCancelledByDriver {
			logger.TripEvent("trip_cancel_driver", tripID, "noop", logger.TripEventAttrs(driverUserID, riderUserID)...)
			return &TripActionResult{Result: "noop", Status: domain.TripStatusCancelledByDriver}, nil
		}
		return nil, domain.ErrInvalidTransition
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE drivers SET is_active = CASE WHEN COALESCE(manual_offline,0) = 0 THEN 1 ELSE is_active END WHERE user_id = ?1`, driverUserID)
	if riderUserID != 0 {
		var telegramID int64
		if err := s.db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, riderUserID).Scan(&telegramID); err == nil {
			msg := tgbotapi.NewMessage(telegramID, "Haydovchi safarni bekor qildi.")
			_, _ = s.riderBot.Send(msg)
			// Restore rider main menu keyboard (same as no active trip) so bottom buttons are not stuck on active-trip state
			mainMenu := tgbotapi.NewReplyKeyboard(
				tgbotapi.NewKeyboardButtonRow(
					tgbotapi.NewKeyboardButton("🚕 Taxi chaqirish"),
					tgbotapi.NewKeyboardButton("ℹ️ Yordam"),
				),
			)
			mainMenu.ResizeKeyboard = true
			kbMsg := tgbotapi.NewMessage(telegramID, "Yangi so'rov uchun «Taxi chaqirish» ni bosing.")
			kbMsg.ReplyMarkup = mainMenu
			_, _ = s.riderBot.Send(kbMsg)
		}
	}
	if s.hub != nil {
		s.hub.BroadcastToTrip(tripID, ws.Event{Type: "trip_cancelled", TripStatus: domain.TripStatusCancelledByDriver, Payload: map[string]string{"by": "driver"}})
	}
	logger.TripEvent("trip_cancel_driver", tripID, "updated", logger.TripEventAttrs(driverUserID, riderUserID)...)
	return &TripActionResult{Result: "updated", Status: domain.TripStatusCancelledByDriver}, nil
}

// CancelByRider sets trip to CANCELLED_BY_RIDER when status is WAITING or STARTED. Idempotent if already CANCELLED_BY_RIDER.
func (s *TripService) CancelByRider(ctx context.Context, tripID string, riderUserID int64) (*TripActionResult, error) {
	current, err := s.tripRepo.GetStatus(ctx, tripID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrTripNotFound
		}
		return nil, err
	}
	if current == domain.TripStatusCancelledByRider {
		logger.TripEvent("trip_cancel_rider", tripID, "noop", logger.TripEventAttrs(0, riderUserID)...)
		return &TripActionResult{Result: "noop", Status: domain.TripStatusCancelledByRider}, nil
	}
	if err := domain.ValidateTransition(current, domain.TripStatusCancelledByRider); err != nil {
		return nil, err
	}
	n, driverUserID, err := s.tripRepo.CancelByRider(ctx, tripID, riderUserID, nil)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		current, _ = s.tripRepo.GetStatus(ctx, tripID)
		if current == domain.TripStatusCancelledByRider {
			logger.TripEvent("trip_cancel_rider", tripID, "noop", logger.TripEventAttrs(driverUserID, riderUserID)...)
			return &TripActionResult{Result: "noop", Status: domain.TripStatusCancelledByRider}, nil
		}
		return nil, domain.ErrInvalidTransition
	}
	if driverUserID != 0 {
		_, _ = s.db.ExecContext(ctx, `UPDATE drivers SET is_active = CASE WHEN COALESCE(manual_offline,0) = 0 THEN 1 ELSE is_active END WHERE user_id = ?1`, driverUserID)
		var telegramID int64
		if err := s.db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, driverUserID).Scan(&telegramID); err == nil {
			msg := tgbotapi.NewMessage(telegramID, "Mijoz safarni bekor qildi.")
			_, _ = s.driverBot.Send(msg)
		}
	}
	if s.hub != nil {
		s.hub.BroadcastToTrip(tripID, ws.Event{Type: "trip_cancelled", TripStatus: domain.TripStatusCancelledByRider, Payload: map[string]string{"by": "rider"}})
	}
	logger.TripEvent("trip_cancel_rider", tripID, "updated", logger.TripEventAttrs(driverUserID, riderUserID)...)
	return &TripActionResult{Result: "updated", Status: domain.TripStatusCancelledByRider}, nil
}
