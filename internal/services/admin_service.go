package services

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"time"

	"taxi-mvp/internal/accounting"
	"taxi-mvp/internal/models"
	"taxi-mvp/internal/repositories"
)

// DashboardSummary is returned by the admin dashboard endpoint.
type DashboardSummary struct {
	TotalDrivers          int64 `json:"total_drivers"`
	ActiveDrivers         int64 `json:"active_drivers"`
	InactiveDrivers       int64 `json:"inactive_drivers"`
	TotalDriverBalances   int64 `json:"total_driver_balances"`   // promo + cash (compat)
	TotalPromoBalances    int64 `json:"total_promo_balances"`    // platform promotional credit only
	TotalCashBalances     int64 `json:"total_cash_balances"`     // real-wallet leg
	TodaysTrips           int64 `json:"todays_trips"`
}

// AdminService coordinates admin-facing driver, payment, and dashboard operations.
type AdminService struct {
	db       *sql.DB
	drivers  repositories.AdminDriverRepository
	payments repositories.PaymentRepository
	trips    repositories.TripStatsRepository
	ledger   *repositories.DriverLedgerRepository
}

// NewAdminService constructs an AdminService.
func NewAdminService(
	db *sql.DB,
	drivers repositories.AdminDriverRepository,
	payments repositories.PaymentRepository,
	trips repositories.TripStatsRepository,
) *AdminService {
	return &AdminService{
		db:       db,
		drivers:  drivers,
		payments: payments,
		trips:    trips,
		ledger:   repositories.NewDriverLedgerRepository(db),
	}
}

// ListDrivers returns admin DTOs with computed ACTIVE/INACTIVE status.
func (s *AdminService) ListDrivers(ctx context.Context) ([]models.AdminDriverDTO, error) {
	ds, err := s.drivers.ListDriversWithBalance(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.AdminDriverDTO, 0, len(ds))
	for _, d := range ds {
		status := "INACTIVE"
		if d.Balance > 0 {
			status = "ACTIVE"
		}
		out = append(out, models.AdminDriverDTO{
			DriverID:                     d.ID,
			Name:                         d.Name,
			Phone:                        d.Phone,
			CarModel:                     d.CarModel,
			PlateNumber:                  d.PlateNumber,
			PromoBalance:                 d.PromoBalance,
			CashBalance:                  d.CashBalance,
			Balance:                      d.Balance,
			TotalPaid:                    d.TotalPaid,
			Status:                       status,
			VerificationStatus:           d.VerificationStatus,
			DriverTermsOK:                d.HasDriverTerms != 0,
			UserTermsOK:                  d.HasUserTerms != 0,
			PrivacyOK:                    d.HasPrivacy != 0,
			DriverTermsAcceptedVersion:   d.AcceptedDriverTermsVersion,
			UserTermsAcceptedVersion:     d.AcceptedUserTermsVersion,
			PrivacyPolicyAcceptedVersion: d.AcceptedPrivacyVersion,
			LegacyUserTermsFlag:          d.UserTermsAcceptedLegacy,
			LegacyDriverTermsFlag:        d.DriverTermsLegacy,
		})
	}
	return out, nil
}

// ListRiders returns riders for the admin dashboard (Foydalanuvchilar).
func (s *AdminService) ListRiders(ctx context.Context) ([]models.AdminRiderDTO, error) {
	return s.drivers.ListRidersForAdmin(ctx)
}

// ListDriverLedger returns recent driver_ledger rows (audit: promo vs cash).
func (s *AdminService) ListDriverLedger(ctx context.Context, driverID int64, limit int) ([]models.DriverLedgerEntry, error) {
	if s.ledger == nil {
		return nil, nil
	}
	return s.ledger.ListByDriver(ctx, driverID, limit)
}

// SetDriverVerification sets verification_status to "approved" or "rejected". Returns the driver's Telegram ID for notification.
func (s *AdminService) SetDriverVerification(ctx context.Context, driverUserID int64, status string) (telegramID int64, err error) {
	if status != "approved" && status != "rejected" {
		return 0, nil
	}
	if err := s.drivers.UpdateVerificationStatus(ctx, driverUserID, status); err != nil {
		return 0, err
	}
	if status == "approved" {
		if err := accounting.TryGrantSignupPromoOnce(ctx, s.db, driverUserID); err != nil {
			log.Printf("admin_service: signup promo on approve user_id=%d: %v", driverUserID, err)
		}
	}
	telegramID, err = s.drivers.GetDriverTelegramID(ctx, driverUserID)
	return telegramID, err
}

// AddDriverBalance records a cash-wallet top-up only (not promotional credit). Creates driver_ledger CASH_TOPUP + payments deposit.
func (s *AdminService) AddDriverBalance(ctx context.Context, driverID int64, amountCents int64, note string) error {
	if amountCents <= 0 {
		return nil
	}
	if s.db == nil {
		return nil
	}
	return accounting.GrantCashTopUp(ctx, s.db, s.payments, driverID, amountCents, note)
}

// AdjustDriverBalance applies a signed manual delta to the driver's cash wallet and total balance, and records an audit ledger entry.
// Does not create a payments row (admin-side correction only).
// Returns final balances and is_active flag after adjustment.
func (s *AdminService) AdjustDriverBalance(ctx context.Context, driverID int64, delta int64, reason string, adminID int64) (promoBalance, cashBalance, totalBalance int64, isActive int, err error) {
	if delta == 0 || s.db == nil {
		return 0, 0, 0, 0, fmt.Errorf("amount must be non-zero")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var promo, cash, total int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(promo_balance, 0), COALESCE(cash_balance, 0), COALESCE(balance, 0)
		FROM drivers WHERE user_id = ?1`, driverID).Scan(&promo, &cash, &total); err != nil {
		return 0, 0, 0, 0, err
	}
	newTotal := total + delta
	if newTotal < promo {
		return 0, 0, 0, 0, fmt.Errorf("new total balance (%d) cannot be less than promo_balance (%d)", newTotal, promo)
	}
	newCash := newTotal - promo
	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET cash_balance = ?1, balance = ?2,
		  is_active = CASE WHEN ?2 <= 0 THEN 0 ELSE is_active END
		WHERE user_id = ?3`,
		newCash, newTotal, driverID); err != nil {
		return 0, 0, 0, 0, err
	}

	ledger := repositories.NewDriverLedgerRepository(s.db)
	refType := "admin_adjust"
	refID := "admin:" + strconv.FormatInt(adminID, 10)
	note := reason
	if note == "" {
		note = "Admin manual balance adjustment"
	}
	e := &models.DriverLedgerEntry{
		DriverID:      driverID,
		Bucket:        models.LedgerBucketCash,
		EntryType:     models.LedgerEntryManualAdjustment,
		Amount:        delta,
		ReferenceType: &refType,
		ReferenceID:   &refID,
		Note:          &note,
	}
	if err := ledger.InsertTx(ctx, tx, e); err != nil {
		return 0, 0, 0, 0, err
	}

	// Read back final balances and is_active for response.
	var promoFinal, cashFinal, totalFinal int64
	var isActiveFinal int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(promo_balance,0), COALESCE(cash_balance,0), COALESCE(balance,0), COALESCE(is_active,0)
		FROM drivers WHERE user_id = ?1`, driverID).Scan(&promoFinal, &cashFinal, &totalFinal, &isActiveFinal); err != nil {
		return 0, 0, 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, 0, 0, err
	}
	return promoFinal, cashFinal, totalFinal, isActiveFinal, nil
}

// ListPayments returns payment history, optionally filtered by driver.
func (s *AdminService) ListPayments(ctx context.Context, driverID *int64) ([]models.Payment, error) {
	return s.payments.ListPayments(ctx, driverID, nil, nil)
}

// GetDashboard computes summary stats for the admin dashboard.
func (s *AdminService) GetDashboard(ctx context.Context) (*DashboardSummary, error) {
	ds, err := s.drivers.ListDriversWithBalance(ctx)
	if err != nil {
		return nil, err
	}
	var summary DashboardSummary
	for _, d := range ds {
		summary.TotalDrivers++
		if d.Balance > 0 {
			summary.ActiveDrivers++
		} else {
			summary.InactiveDrivers++
		}
		summary.TotalDriverBalances += d.Balance
		summary.TotalPromoBalances += d.PromoBalance
		summary.TotalCashBalances += d.CashBalance
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	tripsToday, err := s.trips.CountTripsForDay(ctx, day)
	if err != nil {
		return nil, err
	}
	summary.TodaysTrips = tripsToday
	return &summary, nil
}
