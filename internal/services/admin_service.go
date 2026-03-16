package services

import (
	"context"
	"time"

	"taxi-mvp/internal/models"
	"taxi-mvp/internal/repositories"
)

// DashboardSummary is returned by the admin dashboard endpoint.
type DashboardSummary struct {
	TotalDrivers        int64 `json:"total_drivers"`
	ActiveDrivers       int64 `json:"active_drivers"`
	InactiveDrivers     int64 `json:"inactive_drivers"`
	TotalDriverBalances int64 `json:"total_driver_balances"`
	TodaysTrips         int64 `json:"todays_trips"`
}

// AdminService coordinates admin-facing driver, payment, and dashboard operations.
type AdminService struct {
	drivers  repositories.AdminDriverRepository
	payments repositories.PaymentRepository
	trips    repositories.TripStatsRepository
}

// NewAdminService constructs an AdminService.
func NewAdminService(
	drivers repositories.AdminDriverRepository,
	payments repositories.PaymentRepository,
	trips repositories.TripStatsRepository,
) *AdminService {
	return &AdminService{
		drivers:  drivers,
		payments: payments,
		trips:    trips,
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
			Balance:            d.Balance,
			TotalPaid:          d.TotalPaid,
			Status:             status,
			VerificationStatus: d.VerificationStatus,
		})
	}
	return out, nil
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

// AddDriverBalance records a positive deposit to a driver's balance.
// amountCents must be > 0 and is in the smallest currency units.
func (s *AdminService) AddDriverBalance(ctx context.Context, driverID int64, amountCents int64, note string) error {
	if amountCents <= 0 {
		return nil
	}
	// Increase balance and total_paid.
	if err := s.drivers.UpdateDriverBalance(ctx, driverID, amountCents, true); err != nil {
		return err
	}
	p := &models.Payment{
		DriverID: driverID,
		Amount:   amountCents,
		Type:     models.PaymentTypeDeposit,
		Note:     note,
	}
	return s.payments.InsertPayment(ctx, p)
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
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	tripsToday, err := s.trips.CountTripsForDay(ctx, day)
	if err != nil {
		return nil, err
	}
	summary.TodaysTrips = tripsToday
	return &summary, nil
}

