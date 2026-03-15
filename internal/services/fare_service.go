package services

import (
	"context"
	"database/sql"

	"taxi-mvp/internal/config"
	"taxi-mvp/internal/models"
	"taxi-mvp/internal/repositories"
	"taxi-mvp/internal/utils"
)

// FareService provides fare settings (DB with config fallback) and tiered fare calculation.
type FareService struct {
	repo *repositories.FareSettingsRepo
	cfg  *config.Config
}

// NewFareService returns a FareService.
func NewFareService(db *sql.DB, cfg *config.Config) *FareService {
	return &FareService{repo: repositories.NewFareSettingsRepo(db), cfg: cfg}
}

// GetFareSettings returns the active fare settings from DB. If no row, returns defaults from config.
func (s *FareService) GetFareSettings(ctx context.Context) (*models.FareSettings, error) {
	settings, err := s.repo.GetFareSettings(ctx)
	if err == nil {
		return settings, nil
	}
	if err == sql.ErrNoRows && s.cfg != nil {
		pc := s.cfg.CommissionPercent
		if pc <= 0 {
			pc = 5
		}
		return &models.FareSettings{
			ID:               1,
			BaseFare:         int64(s.cfg.StartingFee),
			Tier0_1Km:        int64(s.cfg.PricePerKm),
			Tier1_2Km:        int64(s.cfg.PricePerKm),
			Tier2PlusKm:      int64(s.cfg.PricePerKm),
			CommissionPercent: pc,
		}, nil
	}
	return nil, err
}

// CalculateFare returns the fare (rounded integer) for the given distance_km using current tiered settings.
func (s *FareService) CalculateFare(ctx context.Context, distanceKm float64) (int64, error) {
	settings, err := s.GetFareSettings(ctx)
	if err != nil {
		return 0, err
	}
	return utils.CalculateFareTiered(
		float64(settings.BaseFare),
		float64(settings.Tier0_1Km),
		float64(settings.Tier1_2Km),
		float64(settings.Tier2PlusKm),
		distanceKm,
	), nil
}

// UpdateBaseFare updates base_fare. updatedBy is the admin telegram user id.
func (s *FareService) UpdateBaseFare(ctx context.Context, value int64, updatedBy int64) (int64, error) {
	return s.repo.UpdateBaseFare(ctx, value, updatedBy)
}

// UpdateTier0To1 updates tier_0_1_km.
func (s *FareService) UpdateTier0To1(ctx context.Context, value int64, updatedBy int64) (int64, error) {
	return s.repo.UpdateTier0To1(ctx, value, updatedBy)
}

// UpdateTier1To2 updates tier_1_2_km.
func (s *FareService) UpdateTier1To2(ctx context.Context, value int64, updatedBy int64) (int64, error) {
	return s.repo.UpdateTier1To2(ctx, value, updatedBy)
}

// UpdateTier2Plus updates tier_2_plus_km.
func (s *FareService) UpdateTier2Plus(ctx context.Context, value int64, updatedBy int64) (int64, error) {
	return s.repo.UpdateTier2Plus(ctx, value, updatedBy)
}

// UpdateCommissionPercent updates commission_percent (0-100). updatedBy is the admin telegram user id.
func (s *FareService) UpdateCommissionPercent(ctx context.Context, value int, updatedBy int64) (int64, error) {
	return s.repo.UpdateCommissionPercent(ctx, value, updatedBy)
}
