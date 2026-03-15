package repositories

import (
	"context"
	"database/sql"

	"taxi-mvp/internal/models"
)

// FareSettingsRepo reads/updates the single fare_settings row (id=1).
type FareSettingsRepo struct {
	db *sql.DB
}

// NewFareSettingsRepo returns a FareSettingsRepo.
func NewFareSettingsRepo(db *sql.DB) *FareSettingsRepo {
	return &FareSettingsRepo{db: db}
}

// GetFareSettings returns the active fare settings (id=1). Returns sql.ErrNoRows if not found.
func (r *FareSettingsRepo) GetFareSettings(ctx context.Context) (*models.FareSettings, error) {
	var s models.FareSettings
	var updatedBy sql.NullInt64
	var commissionPercent int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, base_fare, tier_0_1_km, tier_1_2_km, tier_2_plus_km, COALESCE(commission_percent, 5), updated_at, updated_by
		FROM fare_settings WHERE id = 1`,
	).Scan(&s.ID, &s.BaseFare, &s.Tier0_1Km, &s.Tier1_2Km, &s.Tier2PlusKm, &commissionPercent, &s.UpdatedAt, &updatedBy)
	if err != nil {
		return nil, err
	}
	s.CommissionPercent = commissionPercent
	if updatedBy.Valid {
		s.UpdatedBy = &updatedBy.Int64
	}
	return &s, nil
}

// UpdateBaseFare sets base_fare and updated_at, updated_by for id=1. Returns rows affected.
func (r *FareSettingsRepo) UpdateBaseFare(ctx context.Context, value int64, updatedBy int64) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE fare_settings SET base_fare = ?1, updated_at = datetime('now'), updated_by = ?2 WHERE id = 1`,
		value, updatedBy)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdateTier0To1 sets tier_0_1_km for id=1.
func (r *FareSettingsRepo) UpdateTier0To1(ctx context.Context, value int64, updatedBy int64) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE fare_settings SET tier_0_1_km = ?1, updated_at = datetime('now'), updated_by = ?2 WHERE id = 1`,
		value, updatedBy)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdateTier1To2 sets tier_1_2_km for id=1.
func (r *FareSettingsRepo) UpdateTier1To2(ctx context.Context, value int64, updatedBy int64) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE fare_settings SET tier_1_2_km = ?1, updated_at = datetime('now'), updated_by = ?2 WHERE id = 1`,
		value, updatedBy)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdateTier2Plus sets tier_2_plus_km for id=1.
func (r *FareSettingsRepo) UpdateTier2Plus(ctx context.Context, value int64, updatedBy int64) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE fare_settings SET tier_2_plus_km = ?1, updated_at = datetime('now'), updated_by = ?2 WHERE id = 1`,
		value, updatedBy)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdateCommissionPercent sets commission_percent for id=1. Value should be 0-100.
func (r *FareSettingsRepo) UpdateCommissionPercent(ctx context.Context, value int, updatedBy int64) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE fare_settings SET commission_percent = ?1, updated_at = datetime('now'), updated_by = ?2 WHERE id = 1`,
		value, updatedBy)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
