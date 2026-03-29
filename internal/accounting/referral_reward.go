package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/models"
	"taxi-mvp/internal/repositories"
)

// Driver referral reward (YettiQanot): inviter +20_000 promo when referred driver finishes 3 trips (once per referred).
const (
	ReferralRewardPromoSoM = int64(20_000)
	RefTypeReferralReward  = "referral_reward"
	ReferralRewardTripN    = 3

	ReferralRewardReasonSuccess             = "success"
	ReferralRewardReasonAlreadyGranted      = "already_granted"
	ReferralRewardReasonNoInviter           = "no_inviter"
	ReferralRewardReasonNotEnoughTrips      = "not_enough_trips"
	ReferralRewardReasonPastThirdTrip       = "past_third_trip"
	ReferralRewardReasonEmptyTripID         = "empty_trip_id"
	ReferralRewardReasonReferredNotApproved = "referred_not_approved"
	ReferralRewardReasonInviterNotDriver    = "inviter_not_driver"
	ReferralRewardReasonSelfReferral        = "self_referral"
	ReferralRewardReasonDBError             = "db_error"
)

// ReferralRewardResult is returned by TryGrantReferralReward (Telegram only if Granted && InviterTelegramID set).
type ReferralRewardResult struct {
	Granted             bool   `json:"granted"`
	UpdatedPromoBalance int64  `json:"updated_promo_balance"`
	Reason              string `json:"reason"`
	InviterUserID       int64  `json:"inviter_user_id,omitempty"`
	InviterTelegramID   int64  `json:"-"`
}

// GetInviterForDriver returns inviter users.id for a referred driver, from driver_referrals or users.referred_by.
func GetInviterForDriver(ctx context.Context, q DBTX, referredUserID int64) (inviterUserID int64, ok bool, err error) {
	var id sql.NullInt64
	err = q.QueryRowContext(ctx, `
		SELECT inviter_user_id FROM driver_referrals WHERE referred_user_id = ?1`, referredUserID).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return 0, false, err
	}
	if err == nil && id.Valid && id.Int64 > 0 {
		return id.Int64, true, nil
	}
	var code sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT referred_by FROM users WHERE id = ?1`, referredUserID).Scan(&code); err != nil {
		return 0, false, err
	}
	if !code.Valid || strings.TrimSpace(code.String) == "" {
		return 0, false, nil
	}
	var inv int64
	if err := q.QueryRowContext(ctx, `SELECT id FROM users WHERE referral_code = ?1`, strings.TrimSpace(code.String)).Scan(&inv); err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, err
	}
	if inv == referredUserID {
		return 0, false, nil
	}
	return inv, true, nil
}

// CountFinishedTripsForDriver counts trips in FINISHED status for the driver user_id.
func CountFinishedTripsForDriver(ctx context.Context, db *sql.DB, driverUserID int64) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trips WHERE driver_user_id = ?1 AND status = ?2`,
		driverUserID, domain.TripStatusFinished).Scan(&n)
	return n, err
}

// HasReferralRewardBeenGranted is true if inviter already has a referral_reward ledger row for this referred user.
func HasReferralRewardBeenGranted(ctx context.Context, db *sql.DB, inviterUserID, referredUserID int64) (bool, error) {
	rid := strconv.FormatInt(referredUserID, 10)
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM driver_ledger
		WHERE driver_id = ?1 AND entry_type = ?2 AND reference_type = ?3 AND reference_id = ?4`,
		inviterUserID, models.LedgerEntryPromoGranted, RefTypeReferralReward, rid).Scan(&n)
	return n > 0, err
}

// RecordDriverReferral ensures driver_referrals has a row when referred_by points at a valid inviter (idempotent).
func RecordDriverReferral(ctx context.Context, db *sql.DB, referredUserID int64, referralCode string) error {
	code := strings.TrimSpace(referralCode)
	if code == "" {
		return nil
	}
	var inviterID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE referral_code = ?1`, code).Scan(&inviterID); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if inviterID == referredUserID {
		return nil
	}
	_, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO driver_referrals (inviter_user_id, referred_user_id) VALUES (?1, ?2)`,
		inviterID, referredUserID)
	return err
}

// grantReferralRewardInTx pays the inviter when referredDriverUserID (the driver who finished the trip) has
// exactly 3 FINISHED trips. Counts always use referredDriverUserID, never inviterID.
// Idempotency: INSERT OR IGNORE into driver_ledger (unique driver_id, reference_type, reference_id), then balance + flag.
func grantReferralRewardInTx(ctx context.Context, tx *sql.Tx, db *sql.DB, referredDriverUserID int64, tripID string) (ReferralRewardResult, error) {
	var out ReferralRewardResult
	tripID = strings.TrimSpace(tripID)
	if tripID == "" {
		return out, ErrEmptyTripID
	}

	var tripRowDriver int64
	err := tx.QueryRowContext(ctx, `
		SELECT driver_user_id FROM trips WHERE id = ?1 AND status = ?2`,
		tripID, domain.TripStatusFinished).Scan(&tripRowDriver)
	if err != nil {
		if err == sql.ErrNoRows {
			out.Reason = ReferralRewardReasonDBError
			log.Printf("REFERRAL_SKIP referral_user_id=%d trip_id=%s computed_count=n/a inviter_user_id=n/a reason=trip_missing_or_not_finished", referredDriverUserID, tripID)
			return out, fmt.Errorf("trip not found or not FINISHED: %s", tripID)
		}
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if tripRowDriver != referredDriverUserID {
		log.Printf("REFERRAL_SAFETY_MISMATCH trip_id=%s referral_user_id=%d trip_driver_user_id=%d (count must use referral_user_id only)", tripID, referredDriverUserID, tripRowDriver)
		out.Reason = ReferralRewardReasonDBError
		return out, fmt.Errorf("trip driver_user_id mismatch for referral")
	}

	finishedTripCount, err := FinishedTripCountAfterCompletingTrip(ctx, tx, referredDriverUserID, tripID)
	if err != nil {
		log.Printf("REFERRAL_CHECK referral_user_id=%d trip_id=%s computed_count=- err=%v", referredDriverUserID, tripID, err)
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	log.Printf("REFERRAL_CHECK referral_user_id=%d trip_id=%s computed_count=%d need_exactly=%d", referredDriverUserID, tripID, finishedTripCount, ReferralRewardTripN)

	if finishedTripCount < ReferralRewardTripN {
		out.Reason = ReferralRewardReasonNotEnoughTrips
		log.Printf("REFERRAL_SKIP referral_user_id=%d trip_id=%s computed_count=%d reason=not_enough_trips", referredDriverUserID, tripID, finishedTripCount)
		return out, nil
	}
	if finishedTripCount > ReferralRewardTripN {
		out.Reason = ReferralRewardReasonPastThirdTrip
		log.Printf("REFERRAL_SKIP referral_user_id=%d trip_id=%s computed_count=%d reason=past_third_trip", referredDriverUserID, tripID, finishedTripCount)
		return out, nil
	}

	inviterID, hasInviter, err := GetInviterForDriver(ctx, tx, referredDriverUserID)
	if err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if !hasInviter || inviterID <= 0 {
		out.Reason = ReferralRewardReasonNoInviter
		log.Printf("REFERRAL_SKIP referral_user_id=%d trip_id=%s computed_count=%d inviter_user_id=0 reason=no_inviter", referredDriverUserID, tripID, finishedTripCount)
		return out, nil
	}
	log.Printf("REFERRAL_CHECK referral_user_id=%d trip_id=%s inviter_user_id=%d computed_count=%d", referredDriverUserID, tripID, inviterID, finishedTripCount)

	if inviterID == referredDriverUserID {
		out.Reason = ReferralRewardReasonSelfReferral
		log.Printf("REFERRAL_SKIP referral_user_id=%d trip_id=%s inviter_user_id=%d reason=self_referral", referredDriverUserID, tripID, inviterID)
		return out, nil
	}

	var ver string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(verification_status, '') FROM drivers WHERE user_id = ?1`, referredDriverUserID).Scan(&ver); err != nil {
		if err == sql.ErrNoRows {
			out.Reason = ReferralRewardReasonReferredNotApproved
			return out, nil
		}
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if strings.TrimSpace(ver) != "approved" {
		out.Reason = ReferralRewardReasonReferredNotApproved
		log.Printf("REFERRAL_SKIP referral_user_id=%d inviter_user_id=%d trip_id=%s reason=referred_not_approved", referredDriverUserID, inviterID, tripID)
		return out, nil
	}

	var invDriver int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM drivers WHERE user_id = ?1`, inviterID).Scan(&invDriver); err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if invDriver == 0 {
		out.Reason = ReferralRewardReasonInviterNotDriver
		log.Printf("REFERRAL_SKIP referral_user_id=%d inviter_user_id=%d trip_id=%s reason=inviter_not_driver", referredDriverUserID, inviterID, tripID)
		return out, nil
	}

	meta, _ := json.Marshal(map[string]interface{}{
		metaSourceKey:                 "referral_reward",
		"program":                     "yettiqanot_driver_v2026",
		"trigger_finished_trip_count": ReferralRewardTripN,
	})
	metaStr := string(meta)
	note := "Referral reward: invited driver completed 3 finished trips (promo credit only; not cash; not withdrawable)."
	refT := RefTypeReferralReward
	rid := strconv.FormatInt(referredDriverUserID, 10)
	ledger := repositories.NewDriverLedgerRepository(db)
	inserted, err := ledger.InsertTxOrIgnore(ctx, tx, &models.DriverLedgerEntry{
		DriverID:      inviterID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryPromoGranted,
		Amount:        ReferralRewardPromoSoM,
		ReferenceType: &refT,
		ReferenceID:   &rid,
		Note:          &note,
		MetadataJSON:  &metaStr,
	})
	if err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if !inserted {
		out.Reason = ReferralRewardReasonAlreadyGranted
		log.Printf("REFERRAL_SKIP referral_user_id=%d inviter_user_id=%d trip_id=%s computed_count=%d reason=insert_or_ignore_duplicate", referredDriverUserID, inviterID, tripID, finishedTripCount)
		return out, nil
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET promo_balance = promo_balance + ?1, balance = balance + ?1 WHERE user_id = ?2`,
		ReferralRewardPromoSoM, inviterID); err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}

	resFlag, err := tx.ExecContext(ctx, `
		UPDATE users SET referral_stage2_reward_paid = 1
		WHERE id = ?1 AND COALESCE(referral_stage2_reward_paid, 0) = 0`, referredDriverUserID)
	if err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if n, _ := resFlag.RowsAffected(); n == 0 {
		log.Printf("REFERRAL_WARN referral_user_id=%d inviter_user_id=%d trip_id=%s ledger_inserted_but_referral_flag_already_set", referredDriverUserID, inviterID, tripID)
	}

	var promo int64
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(promo_balance, 0) FROM drivers WHERE user_id = ?1`, inviterID).Scan(&promo)
	var tg int64
	_ = tx.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, inviterID).Scan(&tg)
	out.Granted = true
	out.Reason = ReferralRewardReasonSuccess
	out.UpdatedPromoBalance = promo
	out.InviterUserID = inviterID
	out.InviterTelegramID = tg
	log.Printf("REFERRAL_GRANTED inviter_user_id=%d referral_user_id=%d trip_id=%s computed_count=%d amount=%d promo_balance=%d", inviterID, referredDriverUserID, tripID, finishedTripCount, ReferralRewardPromoSoM, promo)
	return out, nil
}

// TryGrantReferralReward runs grantReferralRewardInTx in its own transaction (tests / isolated use).
func TryGrantReferralReward(ctx context.Context, db *sql.DB, referredUserID int64, tripID string) (ReferralRewardResult, error) {
	tripID = strings.TrimSpace(tripID)
	if tripID == "" {
		return ReferralRewardResult{Reason: ReferralRewardReasonEmptyTripID}, ErrEmptyTripID
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ReferralRewardResult{Reason: ReferralRewardReasonDBError}, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err := grantReferralRewardInTx(ctx, tx, db, referredUserID, tripID)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return ReferralRewardResult{Reason: ReferralRewardReasonDBError}, err
	}
	return out, nil
}

// ReferralRewardInviterTelegramMessage is sent to the inviter after a successful grant (Uzbek).
func ReferralRewardInviterTelegramMessage(promoBalance int64) string {
	return fmt.Sprintf("🎉 Tabriklaymiz!\n\nSiz taklif qilgan haydovchi 3 ta safarni yakunladi\n\n🎁 Sizga +20 000 promo kredit berildi\n\n💰 Jami promo balans: %d", promoBalance)
}

// DriverReferralStatus is optional API payload for the referred driver’s progress.
type DriverReferralStatus struct {
	InviterUserID         int64 `json:"inviter_user_id"`
	FinishedTripCount     int64 `json:"finished_trip_count"`
	RewardThreshold       int   `json:"reward_threshold"`
	ReferralRewardGranted bool  `json:"referral_reward_granted"`
}

// GetReferredDriverReferralStatus returns referral progress for userID when they are a referred driver.
func GetReferredDriverReferralStatus(ctx context.Context, db *sql.DB, driverUserID int64) (DriverReferralStatus, error) {
	var st DriverReferralStatus
	st.RewardThreshold = ReferralRewardTripN
	inviter, ok, err := GetInviterForDriver(ctx, db, driverUserID)
	if err != nil {
		return st, err
	}
	if !ok {
		return st, nil
	}
	st.InviterUserID = inviter
	n, err := CountFinishedTripsForDriver(ctx, db, driverUserID)
	if err != nil {
		return st, err
	}
	st.FinishedTripCount = n
	var paid int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(referral_stage2_reward_paid, 0) FROM users WHERE id = ?1`, driverUserID).Scan(&paid)
	st.ReferralRewardGranted = paid != 0
	if !st.ReferralRewardGranted {
		g, err := HasReferralRewardBeenGranted(ctx, db, inviter, driverUserID)
		if err == nil && g {
			st.ReferralRewardGranted = true
		}
	}
	return st, nil
}
