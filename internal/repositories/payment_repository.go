package repositories

import (
	"context"
	"database/sql"
	"time"

	"taxi-mvp/internal/models"
)

// PaymentRepository defines operations on the payments ledger.
type PaymentRepository interface {
	InsertPayment(ctx context.Context, p *models.Payment) error
	ListPayments(ctx context.Context, driverID *int64, from, to *time.Time) ([]models.Payment, error)
}

type paymentRepo struct {
	db *sql.DB
}

// NewPaymentRepository returns a PaymentRepository backed by *sql.DB.
func NewPaymentRepository(db *sql.DB) PaymentRepository {
	return &paymentRepo{db: db}
}

// InsertPayment inserts a new payment row and populates ID and CreatedAt on the struct.
func (r *paymentRepo) InsertPayment(ctx context.Context, p *models.Payment) error {
	var res sql.Result
	var err error
	if p.TripID != nil && *p.TripID != "" {
		res, err = r.db.ExecContext(ctx, `
			INSERT INTO payments (driver_id, amount, type, note, trip_id)
			VALUES (?1, ?2, ?3, ?4, ?5)`,
			p.DriverID, p.Amount, string(p.Type), p.Note, *p.TripID)
	} else {
		res, err = r.db.ExecContext(ctx, `
			INSERT INTO payments (driver_id, amount, type, note)
			VALUES (?1, ?2, ?3, ?4)`,
			p.DriverID, p.Amount, string(p.Type), p.Note)
	}
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	p.ID = id
	// Load created_at from DB to have authoritative timestamp.
	return r.db.QueryRowContext(ctx, `SELECT created_at FROM payments WHERE id = ?1`, id).
		Scan(&p.CreatedAt)
}

// ListPayments returns payments ordered by created_at DESC, optionally filtered.
// Joins trips to include total_price (trip fare_amount) for trip-related payments.
func (r *paymentRepo) ListPayments(ctx context.Context, driverID *int64, from, to *time.Time) ([]models.Payment, error) {
	query := `SELECT p.id, p.driver_id, p.amount, p.type, p.note, p.created_at, t.fare_amount
	FROM payments p
	LEFT JOIN trips t ON p.trip_id = t.id
	WHERE 1=1`
	var args []interface{}
	if driverID != nil {
		query += " AND p.driver_id = ?"
		args = append(args, *driverID)
	}
	if from != nil {
		query += " AND p.created_at >= ?"
		args = append(args, from.Format(time.RFC3339))
	}
	if to != nil {
		query += " AND p.created_at < ?"
		args = append(args, to.Format(time.RFC3339))
	}
	query += " ORDER BY p.created_at DESC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Payment
	for rows.Next() {
		var p models.Payment
		var typeStr string
		var fareAmount sql.NullInt64
		if err := rows.Scan(&p.ID, &p.DriverID, &p.Amount, &typeStr, &p.Note, &p.CreatedAt, &fareAmount); err != nil {
			return nil, err
		}
		p.Type = models.PaymentType(typeStr)
		if fareAmount.Valid {
			p.TotalPrice = &fareAmount.Int64
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

