package repositories

import (
	"context"
	"database/sql"

	"taxi-mvp/internal/models"
)

// AdminDriverRepository defines read/write operations for admin driver balance views.
type AdminDriverRepository interface {
	ListDriversWithBalance(ctx context.Context) ([]models.Driver, error)
	GetDriverByID(ctx context.Context, id int64) (*models.Driver, error)
	UpdateDriverBalance(ctx context.Context, id int64, delta int64, countPaid bool) error
	SetDriverBalance(ctx context.Context, id int64, newBalance int64) error
}

type adminDriverRepo struct {
	db *sql.DB
}

// NewAdminDriverRepository returns an AdminDriverRepository backed by *sql.DB.
func NewAdminDriverRepository(db *sql.DB) AdminDriverRepository {
	return &adminDriverRepo{db: db}
}

// ListDriversWithBalance returns drivers ordered by user_id DESC with balance and total_paid.
func (r *adminDriverRepo) ListDriversWithBalance(ctx context.Context) ([]models.Driver, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT u.id AS id,
		       COALESCE(u.name, '') AS name,
		       COALESCE(d.phone, '') AS phone,
		       COALESCE(d.car_type, '') AS car_model,
		       COALESCE(d.plate, '') AS plate_number,
		       d.balance,
		       d.total_paid
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		ORDER BY d.user_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Driver
	for rows.Next() {
		var d models.Driver
		if err := rows.Scan(&d.ID, &d.Name, &d.Phone, &d.CarModel, &d.PlateNumber, &d.Balance, &d.TotalPaid); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetDriverByID returns a single driver by user id or nil if not found.
func (r *adminDriverRepo) GetDriverByID(ctx context.Context, id int64) (*models.Driver, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT u.id AS id,
		       COALESCE(u.name, '') AS name,
		       COALESCE(d.phone, '') AS phone,
		       COALESCE(d.car_type, '') AS car_model,
		       COALESCE(d.plate, '') AS plate_number,
		       d.balance,
		       d.total_paid
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		WHERE d.user_id = ?1`, id)
	var d models.Driver
	if err := row.Scan(&d.ID, &d.Name, &d.Phone, &d.CarModel, &d.PlateNumber, &d.Balance, &d.TotalPaid); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

// UpdateDriverBalance adjusts balance (and optionally total_paid) inside a transaction.
func (r *adminDriverRepo) UpdateDriverBalance(ctx context.Context, id int64, delta int64, countPaid bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if countPaid && delta > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE drivers
			SET balance = balance + ?1,
			    total_paid = total_paid + ?1
			WHERE user_id = ?2`, delta, id); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE drivers
			SET balance = balance + ?1
			WHERE user_id = ?2`, delta, id); err != nil {
			return err
		}
	}
	// Sync is_active: ACTIVE (1) if balance > 0, INACTIVE (0) if balance <= 0.
	if _, err := tx.ExecContext(ctx, `
		UPDATE drivers SET is_active = CASE WHEN balance > 0 THEN 1 ELSE 0 END WHERE user_id = ?1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetDriverBalance sets balance to an exact value and syncs is_active (ACTIVE if balance > 0, else INACTIVE).
func (r *adminDriverRepo) SetDriverBalance(ctx context.Context, id int64, newBalance int64) error {
	active := 0
	if newBalance > 0 {
		active = 1
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE drivers SET balance = ?1, is_active = ?2 WHERE user_id = ?3`, newBalance, active, id)
	return err
}

