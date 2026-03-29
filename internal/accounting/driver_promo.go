package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/models"
	"taxi-mvp/internal/repositories"
)

// Driver promo program (YettiQanot): signup once + first 3 finished trips. Promo only; not cash; ledger PROMO_GRANTED.
const (
	DriverSignupPromoSoM   = int64(20_000)
	FirstThreeTripPromoSoM = int64(10_000)
	RefTypeSignupPromo     = "signup_promo"
	RefTypeFirst3TripBonus = "first_3_trip_bonus"
	metaSourceKey          = "source"
	metaTripNumberKey      = "trip_number"
)

// DriverNewPromoProgramMessage is the canonical driver onboarding copy for the current promo program (Uzbek).
const DriverNewPromoProgramMessage = `🎁 Sizga 20 000 promo kredit berildi

🚀 Birinchi 3 ta safar uchun:
har safar +10 000 promo kredit

ℹ️ Promo kredit:
— real pul emas
— naqdlashtirilmaydi
— platforma ichida ishlatiladi`

// DriverPromoProgramStatus is returned for API/bot (GET /driver/promo-program).
type DriverPromoProgramStatus struct {
	SignupPromoGranted           bool  `json:"signup_promo_granted"`
	CompletedTripCount           int64 `json:"completed_trip_count"`
	FirstThreeTripBonusCount     int   `json:"first_three_trip_bonus_count"`
	RemainingFirstTripBonusSlots int   `json:"remaining_first_trip_bonus_slots"`
	PromoBalance                 int64 `json:"promo_balance"`
}

// GetDriverPromoProgramStatus loads promo program progress for a driver user_id.
func GetDriverPromoProgramStatus(ctx context.Context, db *sql.DB, driverUserID int64) (DriverPromoProgramStatus, error) {
	var st DriverPromoProgramStatus
	var signupPaid, promoBal int
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(signup_bonus_paid, 0), COALESCE(promo_balance, 0)
		FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&signupPaid, &promoBal)
	if err != nil {
		return st, err
	}
	st.SignupPromoGranted = signupPaid != 0
	st.PromoBalance = int64(promoBal)

	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trips WHERE driver_user_id = ?1 AND status = ?2`,
		driverUserID, domain.TripStatusFinished).Scan(&st.CompletedTripCount)
	if err != nil {
		return st, err
	}

	var bonusCount int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM driver_ledger
		WHERE driver_id = ?1 AND entry_type = ?2 AND reference_type = ?3`,
		driverUserID, models.LedgerEntryPromoGranted, RefTypeFirst3TripBonus).Scan(&bonusCount)
	if err != nil {
		return st, err
	}
	st.FirstThreeTripBonusCount = bonusCount
	rem := 3 - bonusCount
	if rem < 0 {
		rem = 0
	}
	st.RemainingFirstTripBonusSlots = rem
	return st, nil
}

// GetDriverPromoProgress is an alias for clients that expect this name.
func GetDriverPromoProgress(ctx context.Context, db *sql.DB, driverUserID int64) (DriverPromoProgramStatus, error) {
	return GetDriverPromoProgramStatus(ctx, db, driverUserID)
}

// FinishedTripCountAfterCompletingTrip returns how many trips the driver has with status FINISHED,
// after the given trip row is already FINISHED. The current tripID must belong to the driver and be FINISHED
// so the count includes it (call only after FinishTrip’s UPDATE commits, or inside the same transaction after UPDATE).
func FinishedTripCountAfterCompletingTrip(ctx context.Context, q DBTX, driverUserID int64, tripID string) (int64, error) {
	tid := strings.TrimSpace(tripID)
	if tid == "" {
		return 0, fmt.Errorf("finished trip id required")
	}
	var st string
	err := q.QueryRowContext(ctx, `
		SELECT status FROM trips WHERE id = ?1 AND driver_user_id = ?2`, tid, driverUserID).Scan(&st)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("trip %q not found for driver %d", tid, driverUserID)
		}
		return 0, err
	}
	if st != domain.TripStatusFinished {
		return 0, fmt.Errorf("trip %q not FINISHED (status=%s)", tid, st)
	}
	var n int64
	err = q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trips
		WHERE driver_user_id = ?1 AND status = ?2`,
		driverUserID, domain.TripStatusFinished).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// TryGrantSignupPromoOnce grants 20_000 promo once on approval (signup_bonus_paid gate). Ledger + metadata source signup_promo.
func TryGrantSignupPromoOnce(ctx context.Context, db *sql.DB, driverUserID int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE drivers SET promo_balance = promo_balance + ?1, balance = balance + ?1, signup_bonus_paid = 1
		WHERE user_id = ?2 AND COALESCE(signup_bonus_paid, 0) = 0`,
		DriverSignupPromoSoM, driverUserID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	meta, _ := json.Marshal(map[string]interface{}{
		metaSourceKey: RefTypeSignupPromo,
		"program":     "yettiqanot_driver_v2026",
	})
	metaStr := string(meta)
	note := "Driver signup promotional platform credit (not real money; not withdrawable; promo_balance only)."
	ref := RefTypeSignupPromo
	ledger := repositories.NewDriverLedgerRepository(db)
	if err := ledger.InsertTx(ctx, tx, &models.DriverLedgerEntry{
		DriverID:      driverUserID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryPromoGranted,
		Amount:        DriverSignupPromoSoM,
		ReferenceType: &ref,
		Note:          &note,
		MetadataJSON:  &metaStr,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

const promoProgramKey = "program"
const promoProgramID = "yettiqanot_driver_v2026"

// grantFirstThreeTripPromoInTx grants first-3-trip promo using INSERT OR IGNORE + balance update (same tx).
func grantFirstThreeTripPromoInTx(ctx context.Context, tx *sql.Tx, db *sql.DB, driverUserID int64, tripID string) (granted bool, tripNum int, err error) {
	tripID = strings.TrimSpace(tripID)
	if tripID == "" {
		return false, 0, ErrEmptyTripID
	}
	finishedTripCount, err := FinishedTripCountAfterCompletingTrip(ctx, tx, driverUserID, tripID)
	if err != nil {
		log.Printf("PROMO_CHECK driver_user_id=%d trip_id=%s computed_count=- err=%v", driverUserID, tripID, err)
		return false, 0, err
	}
	log.Printf("PROMO_CHECK driver_user_id=%d trip_id=%s computed_count=%d referral_user_id=%d", driverUserID, tripID, finishedTripCount, driverUserID)
	if finishedTripCount > 3 {
		log.Printf("PROMO_SKIP driver_user_id=%d trip_id=%s computed_count=%d reason=after_first_three", driverUserID, tripID, finishedTripCount)
		return false, 0, nil
	}
	if finishedTripCount < 1 {
		log.Printf("PROMO_SKIP driver_user_id=%d trip_id=%s computed_count=%d reason=invalid_count", driverUserID, tripID, finishedTripCount)
		return false, 0, nil
	}
	tripNum = int(finishedTripCount)

	meta, _ := json.Marshal(map[string]interface{}{
		metaSourceKey:     "first_3_trip_bonus",
		metaTripNumberKey: tripNum,
		promoProgramKey:   promoProgramID,
	})
	metaStr := string(meta)
	note := fmt.Sprintf("First-%d-of-3 trip promotional credit (not real money; not withdrawable).", tripNum)
	refT := RefTypeFirst3TripBonus
	tripCopy := tripID
	ledger := repositories.NewDriverLedgerRepository(db)
	inserted, err := ledger.InsertTxOrIgnore(ctx, tx, &models.DriverLedgerEntry{
		DriverID:      driverUserID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryPromoGranted,
		Amount:        FirstThreeTripPromoSoM,
		ReferenceType: &refT,
		ReferenceID:   &tripCopy,
		Note:          &note,
		MetadataJSON:  &metaStr,
	})
	if err != nil {
		return false, tripNum, err
	}
	if !inserted {
		log.Printf("PROMO_SKIP driver_user_id=%d trip_id=%s computed_count=%d reason=insert_or_ignore_duplicate", driverUserID, tripID, finishedTripCount)
		return false, 0, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET promo_balance = promo_balance + ?1, balance = balance + ?1 WHERE user_id = ?2`,
		FirstThreeTripPromoSoM, driverUserID); err != nil {
		return false, tripNum, err
	}
	log.Printf("PROMO_GRANTED driver_user_id=%d trip_id=%s computed_count=%d trip_num=%d amount=%d", driverUserID, tripID, finishedTripCount, tripNum, FirstThreeTripPromoSoM)
	return true, tripNum, nil
}

// TryGrantFirstThreeTripPromo grants +10_000 promo for finished-trip ordinals 1–3 only, once per trip_id.
// Uses its own transaction (for tests and isolated calls). FinishTrip uses GrantTripFinishPromosAndReferral instead.
func TryGrantFirstThreeTripPromo(ctx context.Context, db *sql.DB, driverUserID int64, tripID string) (granted bool, tripNum int, err error) {
	tripID = strings.TrimSpace(tripID)
	if tripID == "" {
		return false, 0, ErrEmptyTripID
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	g, n, err := grantFirstThreeTripPromoInTx(ctx, tx, db, driverUserID, tripID)
	if err != nil {
		return false, n, err
	}
	if err := tx.Commit(); err != nil {
		return false, n, err
	}
	return g, n, nil
}

// FirstThreeTripBonusTelegramMessage returns trip-finish promo UX copy after a first-3-trip bonus grant (Uzbek).
func FirstThreeTripBonusTelegramMessage(tripNum int, promoBalance int64) string {
	switch tripNum {
	case 1:
		return fmt.Sprintf("🎉 Tabriklaymiz!\n\n1-safaringiz yakunlandi\n+10 000 promo kredit qo‘shildi\n\n💰 Jami promo balans: %d\n\n🚀 Yana 2 ta safar qiling va bonus oling", promoBalance)
	case 2:
		return fmt.Sprintf("🎉 2-safar yakunlandi\n\n+10 000 promo kredit\n\n💰 Balans: %d\n\n🚀 Yana 1 ta safar qoldi", promoBalance)
	case 3:
		return fmt.Sprintf("🔥 Ajoyib!\n\n3-safar yakunlandi\n+10 000 promo kredit\n\n💰 Balans: %d\n\n✅ Barcha boshlang‘ich bonuslar berildi", promoBalance)
	default:
		return ""
	}
}

// BackfillMissingSignupPromos grants signup promo for approved drivers who never received it (schema/repair drift).
func BackfillMissingSignupPromos(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT user_id FROM drivers
		WHERE verification_status = 'approved'
		  AND COALESCE(signup_bonus_paid, 0) = 0`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			continue
		}
		if err := TryGrantSignupPromoOnce(ctx, db, uid); err != nil {
			log.Printf("accounting: backfill signup promo user_id=%d: %v", uid, err)
		}
	}
	return rows.Err()
}
