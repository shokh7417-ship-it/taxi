package services

import (
	"context"
	"database/sql"
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
			DriverID:           d.ID,
			Name:               d.Name,
			Phone:              d.Phone,
			CarModel:           d.CarModel,
			PlateNumber:        d.PlateNumber,
			PromoBalance:       d.PromoBalance,
			CashBalance:        d.CashBalance,
			Balance:            d.Balance,
			TotalPaid:          d.TotalPaid,
			Status:             status,
			VerificationStatus: d.VerificationStatus,
			DriverTermsOK:      d.HasDriverTerms != 0,
			UserTermsOK:        d.HasUserTerms != 0,
			PrivacyOK:          d.HasPrivacy != 0,
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
