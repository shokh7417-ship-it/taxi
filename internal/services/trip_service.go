package services

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/accounting"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/driverloc"
	"taxi-mvp/internal/legal"
	"taxi-mvp/internal/logger"
	"taxi-mvp/internal/repositories"
	"taxi-mvp/internal/utils"
	"taxi-mvp/internal/ws"
)

// Same window as dispatch: only Telegram live location updates count as fresh.
const tripPickupLiveFreshSeconds = 90

func parseDriverLiveAtUTC(s string) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// TripService handles trip lifecycle: start, add points, finish, cancel; notifies rider and driver.
type TripService struct {
	db                   *sql.DB
	tripRepo             *repositories.TripRepo
	riderBot             *tgbotapi.BotAPI
	driverBot            *tgbotapi.BotAPI
	cfg                  *config.Config
	hub                  HubBroadcaster
	fareSvc              *FareService                   // optional; if set, fare comes from DB tiered settings
	payments             repositories.PaymentRepository // optional; legacy payments row on commission; nil skips InsertPaymentTx
	OnDriverStatusUpdate func(telegramID int64)         // optional; e.g. update driver's pinned status panel after trip finish
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

// NewTripService returns a TripService. hub, fareSvc, and payments can be nil.
func NewTripService(db *sql.DB, tripRepo *repositories.TripRepo, riderBot, driverBot *tgbotapi.BotAPI, cfg *config.Config, hub HubBroadcaster, fareSvc *FareService, payments repositories.PaymentRepository) *TripService {
	if tripRepo == nil {
		tripRepo = repositories.NewTripRepo(db)
	}
	return &TripService{db: db, tripRepo: tripRepo, riderBot: riderBot, driverBot: driverBot, cfg: cfg, hub: hub, fareSvc: fareSvc, payments: payments}
}

func (s *TripService) pickupCoordsForTrip(ctx context.Context, tripID string) (pickupLat, pickupLng float64, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT r.pickup_lat, r.pickup_lng FROM trips t
		JOIN ride_requests r ON r.id = t.request_id WHERE t.id = ?1`, tripID).Scan(&pickupLat, &pickupLng)
	return pickupLat, pickupLng, err
}

// ensureDriverNearPickup checks drivers.last_lat/lng against pickup using fresh Telegram live location (same rules as dispatch).
func (s *TripService) ensureDriverNearPickup(ctx context.Context, driverUserID int64, pickupLat, pickupLng float64) error {
	maxM := int64(100)
	if s.cfg != nil && s.cfg.PickupStartMaxMeters > 0 {
		maxM = int64(s.cfg.PickupStartMaxMeters)
	}
	var lastLat, lastLng sql.NullFloat64
	var lastLive sql.NullString
	var liveActive int
	err := s.db.QueryRowContext(ctx, `
		SELECT last_lat, last_lng, last_live_location_at, COALESCE(live_location_active, 0)
		FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&lastLat, &lastLng, &lastLive, &liveActive)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.ErrDriverLocationStale
		}
		return err
	}
	if liveActive != 1 {
		return domain.ErrLiveLocationInactive
	}
	if !lastLive.Valid || lastLive.String == "" {
		return domain.ErrDriverLocationStale
	}
	t, perr := parseDriverLiveAtUTC(lastLive.String)
	if perr != nil {
		return domain.ErrDriverLocationStale
	}
	if time.Since(t) > time.Duration(tripPickupLiveFreshSeconds)*time.Second {
		return domain.ErrDriverLocationStale
	}
	if !lastLat.Valid || !lastLng.Valid {
		return domain.ErrDriverLocationStale
	}
	if utils.HaversineMeters(lastLat.Float64, lastLng.Float64, pickupLat, pickupLng) > float64(maxM) {
		return domain.ErrTooFarFromPickup
	}
	return nil
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

// StartTrip sets trip to STARTED from WAITING or ARRIVED. From WAITING, requires driver near pickup with fresh live location.
// From ARRIVED, proximity is not re-checked (driver already confirmed at pickup). Idempotent if already STARTED.
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
	if current == domain.TripStatusWaiting {
		pickupLat, pickupLng, perr := s.pickupCoordsForTrip(ctx, tripID)
		if perr != nil {
			return nil, perr
		}
		if err := s.ensureDriverNearPickup(ctx, driverUserID, pickupLat, pickupLng); err != nil {
			return nil, err
		}
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

// MarkArrived sets status to ARRIVED from WAITING when the driver is near pickup (same checks as starting from WAITING).
func (s *TripService) MarkArrived(ctx context.Context, tripID string, driverUserID int64) (*TripActionResult, error) {
	current, err := s.tripRepo.GetStatus(ctx, tripID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrTripNotFound
		}
		return nil, err
	}
	if current == domain.TripStatusArrived {
		return &TripActionResult{Result: "noop", Status: domain.TripStatusArrived}, nil
	}
	if err := domain.ValidateTransition(current, domain.TripStatusArrived); err != nil {
		return nil, err
	}
	pickupLat, pickupLng, perr := s.pickupCoordsForTrip(ctx, tripID)
	if perr != nil {
		return nil, perr
	}
	if err := s.ensureDriverNearPickup(ctx, driverUserID, pickupLat, pickupLng); err != nil {
		return nil, err
	}
	n, err := s.tripRepo.UpdateToArrived(ctx, tripID, driverUserID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		current, _ = s.tripRepo.GetStatus(ctx, tripID)
		if current == domain.TripStatusArrived {
			return &TripActionResult{Result: "noop", Status: domain.TripStatusArrived}, nil
		}
		return nil, domain.ErrInvalidTransition
	}
	logger.TripEvent("trip_arrived", tripID, "updated", logger.TripEventAttrs(driverUserID, 0)...)
	return &TripActionResult{Result: "updated", Status: domain.TripStatusArrived}, nil
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
	// Rider bonus/discount is disabled: no referral bonus is applied for riders.
	var riderBonusUsed int64
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
	firstThreeGranted := false
	firstThreeTripNum := 0
	refRewardRes := accounting.ReferralRewardResult{}

	// 1) Trip already FINISHED via UpdateToFinished. 2–4) Bonuses use deterministic count inside accounting (FinishedTripCountAfterCompletingTrip).
	if fc, err := accounting.FinishedTripCountAfterCompletingTrip(ctx, s.db, driverUserID, tripID); err != nil {
		log.Printf("trip_finish: trip=%s driver=%d finishedCount_error=%v", tripID, driverUserID, err)
	} else {
		log.Printf("trip_finish: trip=%s driver=%d finishedCount=%d", tripID, driverUserID, fc)
	}
	// Order: first-3-trip promo → referral → commission.
	if g, tn, err := accounting.TryGrantFirstThreeTripPromo(ctx, s.db, driverUserID, tripID); err != nil {
		log.Printf("trip_service: first_3_trip promo (driver=%d trip=%s): %v", driverUserID, tripID, err)
	} else {
		firstThreeGranted, firstThreeTripNum = g, tn
	}
	var refErr error
	refRewardRes, refErr = accounting.TryGrantReferralReward(ctx, s.db, driverUserID, tripID)
	if refErr != nil {
		log.Printf("trip_service: referral reward (referred=%d): %v", driverUserID, refErr)
		refRewardRes = accounting.ReferralRewardResult{Reason: accounting.ReferralRewardReasonDBError}
	}
	// Internal commission accrual; offset against promo then cash wallet (see driver_ledger); not bank settlement.
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
		commission := (fareAmount * int64(pc)) / 100
		if commission > 0 {
			inf := s.cfg != nil && s.cfg.InfiniteDriverBalance
			if err := accounting.ApplyTripCommission(ctx, s.db, s.payments, driverUserID, tripID, fareAmount, commission, pc, inf); err != nil {
				log.Printf("trip_service: apply trip commission (trip=%s, driver=%d): %v", tripID, driverUserID, err)
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
		// Trip finish: status message + distance/fare; keyboard = live-location help only (online follows live share).
		driverSummary := formatDriverTripCompletionMessage(distanceM, fareAmount)
		kb := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton(driverloc.BtnShareLiveLocation),
			),
		)
		kb.ResizeKeyboard = true
		m := tgbotapi.NewMessage(driverTelegramID, driverSummary)
		m.ReplyMarkup = kb
		if _, err := s.driverBot.Send(m); err != nil {
			log.Printf("trip_service: notify driver finish: %v", err)
		}
		if firstThreeGranted && s.driverBot != nil {
			var promoBal int64
			_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(promo_balance, 0) FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&promoBal)
			if body := accounting.FirstThreeTripBonusTelegramMessage(firstThreeTripNum, promoBal); body != "" {
				if _, err := s.driverBot.Send(tgbotapi.NewMessage(driverTelegramID, body)); err != nil {
					log.Printf("trip_service: notify driver first_3_trip promo: %v", err)
				}
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
			legalOK := legal.NewService(s.db).DriverHasActiveLegal(ctx, driverUserID)
			if !liveRecent || !legalOK {
				_, _ = s.db.ExecContext(ctx, `UPDATE drivers SET is_active = 0 WHERE user_id = ?1`, driverUserID)
			}
		} else {
			_, _ = s.db.ExecContext(ctx, `UPDATE drivers SET is_active = 0 WHERE user_id = ?1`, driverUserID)
		}
		// Live-location reminder only when driver is NOT sharing live, every 3 trips, and location was not just auto-updated (e.g. mini app).
		// Run after a short delay so mini app location update can land first; then we skip reminder if last_seen_at was recently updated.
		if s.OnDriverStatusUpdate != nil {
			s.OnDriverStatusUpdate(driverTelegramID)
		}
	}
	if refRewardRes.Granted && s.driverBot != nil && refRewardRes.InviterTelegramID != 0 {
		body := accounting.ReferralRewardInviterTelegramMessage(refRewardRes.UpdatedPromoBalance)
		if _, err := s.driverBot.Send(tgbotapi.NewMessage(refRewardRes.InviterTelegramID, body)); err != nil {
			log.Printf("trip_service: notify inviter referral reward: %v", err)
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

// normalizeFare returns display fare: if rawFare <= 50 then 0; if rawFare > 50 then round to nearest 100 so'm.
func normalizeFare(rawFare int64) int64 {
	if rawFare <= 50 {
		return 0
	}
	return (rawFare + 50) / 100 * 100
}

func formatTripSummary(distanceM, fareAmount int64, riderBonusUsed int64) string {
	km := float64(distanceM) / 1000
	return fmt.Sprintf("Safar tugadi.\nMasofa: %.2f km\nNarx: %d so'm", km, fareAmount)
}

// formatDriverTripCompletionMessage returns the driver trip completion message (Mini App finish): status + live location hint + distance/fare.
func formatDriverTripCompletionMessage(distanceM, fareAmount int64) string {
	km := float64(distanceM) / 1000
	return fmt.Sprintf("✅ Safar tugadi.\nMasofa: %.2f km\nNarx: %d so'm\n\nYangi buyurtmalar faqat jonli lokatsiya orqali.", km, fareAmount)
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
