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
func GetInviterForDriver(ctx context.Context, db *sql.DB, referredUserID int64) (inviterUserID int64, ok bool, err error) {
	var id sql.NullInt64
	err = db.QueryRowContext(ctx, `
		SELECT inviter_user_id FROM driver_referrals WHERE referred_user_id = ?1`, referredUserID).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return 0, false, err
	}
	if err == nil && id.Valid && id.Int64 > 0 {
		return id.Int64, true, nil
	}
	var code sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT referred_by FROM users WHERE id = ?1`, referredUserID).Scan(&code); err != nil {
		return 0, false, err
	}
	if !code.Valid || strings.TrimSpace(code.String) == "" {
		return 0, false, nil
	}
	var inv int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE referral_code = ?1`, strings.TrimSpace(code.String)).Scan(&inv); err != nil {
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

// TryGrantReferralReward grants +ReferralRewardPromoSoM to the inviter only when the referred driver has
// exactly 3 FINISHED trips (including the trip given by tripID), verification_status approved, and an inviter.
// Count is always FinishedTripCountAfterCompletingTrip — no CountFinishedTripsForDriver fallback on the grant path.
// Idempotency: users.referral_stage2_reward_paid on the referred user + unique driver_ledger (inviter, reference_id=referred id).
func TryGrantReferralReward(ctx context.Context, db *sql.DB, referredUserID int64, tripID string) (ReferralRewardResult, error) {
	var out ReferralRewardResult
	tripID = strings.TrimSpace(tripID)
	if tripID == "" {
		out.Reason = ReferralRewardReasonEmptyTripID
		log.Printf("REFERRAL_SKIP referred=%d reason=empty_trip_id", referredUserID)
		return out, nil
	}
	finishedTripCount, err := FinishedTripCountAfterCompletingTrip(ctx, db, referredUserID, tripID)
	if err != nil {
		log.Printf("REFERRAL_CHECK referred=%d trip=%s err=%v", referredUserID, tripID, err)
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	log.Printf("REFERRAL_CHECK referred=%d trip=%s finished_trip_count=%d need_exactly=%d", referredUserID, tripID, finishedTripCount, ReferralRewardTripN)
	if finishedTripCount < ReferralRewardTripN {
		out.Reason = ReferralRewardReasonNotEnoughTrips
		log.Printf("REFERRAL_SKIP referred=%d trip=%s reason=not_enough_trips count=%d", referredUserID, tripID, finishedTripCount)
		return out, nil
	}
	if finishedTripCount > ReferralRewardTripN {
		out.Reason = ReferralRewardReasonPastThirdTrip
		log.Printf("REFERRAL_SKIP referred=%d trip=%s reason=past_third_trip count=%d", referredUserID, tripID, finishedTripCount)
		return out, nil
	}
	inviterID, hasInviter, err := GetInviterForDriver(ctx, db, referredUserID)
	if err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if !hasInviter || inviterID <= 0 {
		out.Reason = ReferralRewardReasonNoInviter
		log.Printf("REFERRAL_SKIP referred=%d trip=%s count=%d reason=no_inviter", referredUserID, tripID, finishedTripCount)
		return out, nil
	}
	if inviterID == referredUserID {
		out.Reason = ReferralRewardReasonSelfReferral
		log.Printf("REFERRAL_SKIP referred=%d trip=%s reason=self_referral", referredUserID, tripID)
		return out, nil
	}
	var ver string
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(verification_status, '') FROM drivers WHERE user_id = ?1`, referredUserID).Scan(&ver); err != nil {
		if err == sql.ErrNoRows {
			out.Reason = ReferralRewardReasonReferredNotApproved
			return out, nil
		}
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if strings.TrimSpace(ver) != "approved" {
		out.Reason = ReferralRewardReasonReferredNotApproved
		log.Printf("REFERRAL_SKIP referred=%d inviter=%d trip=%s reason=referred_not_approved", referredUserID, inviterID, tripID)
		return out, nil
	}
	var invDriver int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM drivers WHERE user_id = ?1`, inviterID).Scan(&invDriver); err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if invDriver == 0 {
		out.Reason = ReferralRewardReasonInviterNotDriver
		log.Printf("REFERRAL_SKIP referred=%d inviter=%d trip=%s reason=inviter_not_driver", referredUserID, inviterID, tripID)
		return out, nil
	}
	already, err := HasReferralRewardBeenGranted(ctx, db, inviterID, referredUserID)
	if err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if already {
		out.Reason = ReferralRewardReasonAlreadyGranted
		log.Printf("REFERRAL_SKIP referred=%d inviter=%d trip=%s reason=already_granted_ledger", referredUserID, inviterID, tripID)
		return out, nil
	}
	var stage2 int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(referral_stage2_reward_paid, 0) FROM users WHERE id = ?1`, referredUserID).Scan(&stage2)
	if stage2 != 0 {
		out.Reason = ReferralRewardReasonAlreadyGranted
		log.Printf("REFERRAL_SKIP referred=%d inviter=%d trip=%s reason=already_granted_flag", referredUserID, inviterID, tripID)
		return out, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	claim, err := tx.ExecContext(ctx, `
		UPDATE users SET referral_stage2_reward_paid = 1
		WHERE id = ?1 AND COALESCE(referral_stage2_reward_paid, 0) = 0`, referredUserID)
	if err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if n, _ := claim.RowsAffected(); n == 0 {
		out.Reason = ReferralRewardReasonAlreadyGranted
		log.Printf("REFERRAL_SKIP referred=%d inviter=%d trip=%s reason=already_granted_tx_gate", referredUserID, inviterID, tripID)
		return out, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET promo_balance = promo_balance + ?1, balance = balance + ?1 WHERE user_id = ?2`,
		ReferralRewardPromoSoM, inviterID); err != nil {
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	meta, _ := json.Marshal(map[string]interface{}{
		metaSourceKey:                 "referral_reward",
		"program":                     "yettiqanot_driver_v2026",
		"trigger_finished_trip_count": ReferralRewardTripN,
	})
	metaStr := string(meta)
	note := "Referral reward: invited driver completed 3 finished trips (promo credit only; not cash; not withdrawable)."
	refT := RefTypeReferralReward
	rid := strconv.FormatInt(referredUserID, 10)
	ledger := repositories.NewDriverLedgerRepository(db)
	if err := ledger.InsertTx(ctx, tx, &models.DriverLedgerEntry{
		DriverID:      inviterID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryPromoGranted,
		Amount:        ReferralRewardPromoSoM,
		ReferenceType: &refT,
		ReferenceID:   &rid,
		Note:          &note,
		MetadataJSON:  &metaStr,
	}); err != nil {
		if isUniqueConstraintErr(err) {
			out.Reason = ReferralRewardReasonAlreadyGranted
			return out, nil
		}
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	if err := tx.Commit(); err != nil {
		if isUniqueConstraintErr(err) {
			out.Reason = ReferralRewardReasonAlreadyGranted
			return out, nil
		}
		out.Reason = ReferralRewardReasonDBError
		return out, err
	}
	var promo int64
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(promo_balance, 0) FROM drivers WHERE user_id = ?1`, inviterID).Scan(&promo)
	var tg int64
	_ = db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, inviterID).Scan(&tg)
	out.Granted = true
	out.Reason = ReferralRewardReasonSuccess
	out.UpdatedPromoBalance = promo
	out.InviterUserID = inviterID
	out.InviterTelegramID = tg
	log.Printf("REFERRAL_GRANTED inviter=%d referred=%d trip=%s amount=%d promo_balance=%d", inviterID, referredUserID, tripID, ReferralRewardPromoSoM, promo)
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
