// Package accounting applies promo/cash wallet rules and append-only driver_ledger rows.
// Promo credit is platform-only (not withdrawable, not convertible to cash in this codebase).
package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/models"
	"taxi-mvp/internal/repositories"
)

type paymentTxInserter interface {
	InsertPaymentTx(ctx context.Context, tx *sql.Tx, p *models.Payment) error
}

// GrantPromo credits promotional platform balance and records PROMO_GRANTED (not cash, not withdrawable).
func GrantPromo(ctx context.Context, db *sql.DB, driverID int64, amount int64, refType, refID, note string) error {
	if amount == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET promo_balance = promo_balance + ?1, balance = balance + ?1 WHERE user_id = ?2`,
		amount, driverID); err != nil {
		return err
	}
	ledger := repositories.NewDriverLedgerRepository(db)
	refT, refI := strPtr(refType), strPtr(refID)
	n := note
	e := &models.DriverLedgerEntry{
		DriverID:      driverID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryPromoGranted,
		Amount:        amount,
		ReferenceType: refT,
		ReferenceID:   refI,
		Note:          &n,
	}
	if err := ledger.InsertTx(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

// GrantCashTopUp records admin (or future gateway) cash top-up: increases cash_balance only + ledger + payments row.
func GrantCashTopUp(ctx context.Context, db *sql.DB, pay paymentTxInserter, driverID int64, amount int64, note string) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be positive")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET cash_balance = cash_balance + ?1, balance = balance + ?1, total_paid = total_paid + ?1 WHERE user_id = ?2`,
		amount, driverID); err != nil {
		return err
	}
	noteCopy := note
	if noteCopy == "" {
		noteCopy = "Admin cash top-up (internal wallet; not promo credit)"
	}
	e := &models.DriverLedgerEntry{
		DriverID:  driverID,
		Bucket:    models.LedgerBucketCash,
		EntryType: models.LedgerEntryCashTopUp,
		Amount:    amount,
		Note:      &noteCopy,
	}
	ledger := repositories.NewDriverLedgerRepository(db)
	if err := ledger.InsertTx(ctx, tx, e); err != nil {
		return err
	}
	p := &models.Payment{
		DriverID: driverID,
		Amount:   amount,
		Type:     models.PaymentTypeDeposit,
		Note:     noteCopy,
	}
	if err := pay.InsertPaymentTx(ctx, tx, p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET is_active = CASE WHEN (promo_balance + cash_balance) > 0 THEN is_active ELSE 0 END WHERE user_id = ?1`,
		driverID); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyTripCommission records internal commission accrual, offsets against promo then cash, writes ledger rows,
// and optionally inserts a legacy payments row for admin exports. Does not represent bank settlement.
// Skipped when infiniteBalanceMode is true (no deduction, no commission ledger for that trip).
func ApplyTripCommission(ctx context.Context, db *sql.DB, pay paymentTxInserter, driverID int64, tripID string, fareAmount, commission int64, percent int, infiniteBalanceMode bool) error {
	if infiniteBalanceMode || commission <= 0 {
		return nil
	}
	var promoBal, cashBal int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(promo_balance,0), COALESCE(cash_balance,0) FROM drivers WHERE user_id = ?1`, driverID).Scan(&promoBal, &cashBal); err != nil {
		return err
	}
	fromPromo := commission
	if fromPromo > promoBal {
		fromPromo = promoBal
	}
	rem := commission - fromPromo
	fromCash := rem
	if fromCash > cashBal {
		fromCash = cashBal
	}
	uncollected := rem - fromCash

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	meta, _ := json.Marshal(map[string]interface{}{
		"commission_so_m":    commission,
		"fare_so_m":          fareAmount,
		"percent":            percent,
		"promo_offset_so_m":  fromPromo,
		"cash_offset_so_m":   fromCash,
		"uncollected_so_m":   uncollected,
		"internal_accrual":   true,
		"not_cash_settlement": true,
	})
	metaStr := string(meta)
	refTrip := "trip"
	ledger := repositories.NewDriverLedgerRepository(db)
	accrualNote := "Internal platform fee accrued on trip (not a bank payout; offset via promo/cash buckets below)."
	if err := ledger.InsertTx(ctx, tx, &models.DriverLedgerEntry{
		DriverID:      driverID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryCommissionAccrued,
		Amount:        0,
		ReferenceType: &refTrip,
		ReferenceID:   &tripID,
		Note:          &accrualNote,
		MetadataJSON:  &metaStr,
	}); err != nil {
		return err
	}
	if fromPromo > 0 {
		pn := "Promo platform credit applied to internal commission offset (not withdrawal)."
		if err := ledger.InsertTx(ctx, tx, &models.DriverLedgerEntry{
			DriverID:      driverID,
			Bucket:        models.LedgerBucketPromo,
			EntryType:     models.LedgerEntryPromoAppliedToCommission,
			Amount:        -fromPromo,
			ReferenceType: &refTrip,
			ReferenceID:   &tripID,
			Note:          &pn,
		}); err != nil {
			return err
		}
	}
	if fromCash > 0 {
		cn := "Cash wallet applied to internal commission offset (not a bank transfer)."
		if err := ledger.InsertTx(ctx, tx, &models.DriverLedgerEntry{
			DriverID:      driverID,
			Bucket:        models.LedgerBucketCash,
			EntryType:     models.LedgerEntryCashAppliedToCommission,
			Amount:        -fromCash,
			ReferenceType: &refTrip,
			ReferenceID:   &tripID,
			Note:          &cn,
		}); err != nil {
			return err
		}
	}
	deltaPromo := -fromPromo
	deltaCash := -fromCash
	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET
			promo_balance = promo_balance + ?1,
			cash_balance = cash_balance + ?2,
			balance = balance + ?1 + ?2,
			is_active = CASE WHEN (promo_balance + ?1 + cash_balance + ?2) > 0 THEN is_active ELSE 0 END
		WHERE user_id = ?3`,
		deltaPromo, deltaCash, driverID); err != nil {
		return err
	}
	// Legacy admin payments list: commission magnitude (existing dashboards); not a payout record.
	legacyNote := "Internal commission accrual offset against driver wallets (promo/cash); not a bank settlement."
	tripCopy := tripID
	if pay != nil {
		if err := pay.InsertPaymentTx(ctx, tx, &models.Payment{
			DriverID: driverID,
			Amount:   commission,
			Type:     models.PaymentTypeCommission,
			Note:     legacyNote,
			TripID:   &tripCopy,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const fiveTripsBonusSoM = int64(80000)
const referrerStage2SoM = int64(100000)
const driverSignupPromoSoM = int64(100000)

// TryGrantSignupPromoOnce credits startup promotional platform credit once per driver (on approval); not withdrawable cash.
func TryGrantSignupPromoOnce(ctx context.Context, db *sql.DB, driverUserID int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE drivers SET promo_balance = promo_balance + ?1, balance = balance + ?1, signup_bonus_paid = 1
		WHERE user_id = ?2 AND COALESCE(signup_bonus_paid, 0) = 0`,
		driverSignupPromoSoM, driverUserID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	note := "Driver approval startup promotional platform credit (not real money; not withdrawable)."
	ledger := repositories.NewDriverLedgerRepository(db)
	ref := "signup_bonus"
	if err := ledger.InsertTx(ctx, tx, &models.DriverLedgerEntry{
		DriverID:      driverUserID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryPromoGranted,
		Amount:        driverSignupPromoSoM,
		ReferenceType: &ref,
		Note:          &note,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// TryGrantFiveTripsPromoBonus grants 80k promo once when the driver has 5+ finished trips (atomic with five_trips_bonus_paid).
func TryGrantFiveTripsPromoBonus(ctx context.Context, db *sql.DB, driverID int64) (granted bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE drivers SET promo_balance = promo_balance + ?1, balance = balance + ?1, five_trips_bonus_paid = 1
		WHERE user_id = ?2 AND COALESCE(five_trips_bonus_paid, 0) = 0
		  AND (SELECT COUNT(*) FROM trips WHERE driver_user_id = ?2 AND status = ?3) >= 5`,
		fiveTripsBonusSoM, driverID, domain.TripStatusFinished)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	note := "Five finished trips promotional platform credit (not withdrawable cash)."
	ledger := repositories.NewDriverLedgerRepository(db)
	ref := "five_trips_bonus"
	if err := ledger.InsertTx(ctx, tx, &models.DriverLedgerEntry{
		DriverID:      driverID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryPromoGranted,
		Amount:        fiveTripsBonusSoM,
		ReferenceType: &ref,
		Note:          &note,
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// TryGrantReferrerStage2Promo credits the inviter driver with stage-2 promo when the referred driver qualifies.
func TryGrantReferrerStage2Promo(ctx context.Context, db *sql.DB, referredDriverUserID int64, inviterReferralCode string) error {
	if inviterReferralCode == "" {
		return nil
	}
	var inviterUserID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE referral_code = ?1`, inviterReferralCode).Scan(&inviterUserID); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	claim, err := tx.ExecContext(ctx, `
		UPDATE users SET referral_stage2_reward_paid = 1
		WHERE id = ?1 AND COALESCE(referral_stage2_reward_paid, 0) = 0`, referredDriverUserID)
	if err != nil {
		return err
	}
	if n, _ := claim.RowsAffected(); n == 0 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE drivers SET promo_balance = promo_balance + ?1, balance = balance + ?1
		WHERE user_id = ?2`,
		referrerStage2SoM, inviterUserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	note := "Referrer stage-2 promotional platform credit (not withdrawable cash)."
	ref := "referrer_stage2"
	rid := fmt.Sprintf("%d", referredDriverUserID)
	ledger := repositories.NewDriverLedgerRepository(db)
	if err := ledger.InsertTx(ctx, tx, &models.DriverLedgerEntry{
		DriverID:      inviterUserID,
		Bucket:        models.LedgerBucketPromo,
		EntryType:     models.LedgerEntryPromoGranted,
		Amount:        referrerStage2SoM,
		ReferenceType: &ref,
		ReferenceID:   &rid,
		Note:          &note,
	}); err != nil {
		return err
	}
	return tx.Commit()
}
