package repositories

import (
	"context"
	"database/sql"

	"taxi-mvp/internal/domain"
)

// TripRepo performs trip table access (database queries only). All updates are conditional on current status for race safety.
type TripRepo struct {
	db *sql.DB
}

// NewTripRepo returns a TripRepo.
func NewTripRepo(db *sql.DB) *TripRepo {
	return &TripRepo{db: db}
}

// GetStatus returns the current status of the trip, or sql.ErrNoRows if not found.
func (r *TripRepo) GetStatus(ctx context.Context, tripID string) (status string, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT status FROM trips WHERE id = ?1`, tripID).Scan(&status)
	return status, err
}

// UpdateToArrived sets status = ARRIVED, arrived_at = now() only when status = WAITING and driver matches.
func (r *TripRepo) UpdateToArrived(ctx context.Context, tripID string, driverUserID int64) (rowsAffected int64, err error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE trips SET status = ?1, arrived_at = datetime('now')
		WHERE id = ?2 AND driver_user_id = ?3 AND status = ?4`,
		domain.TripStatusArrived, tripID, driverUserID, domain.TripStatusWaiting)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdateToStarted sets status = STARTED, started_at = now() when status is WAITING or ARRIVED and driver matches.
func (r *TripRepo) UpdateToStarted(ctx context.Context, tripID string, driverUserID int64) (rowsAffected int64, err error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE trips SET status = ?1, started_at = datetime('now')
		WHERE id = ?2 AND driver_user_id = ?3 AND status IN (?4, ?5)`,
		domain.TripStatusStarted, tripID, driverUserID, domain.TripStatusWaiting, domain.TripStatusArrived)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdateToFinished sets status = FINISHED, finished_at = now(), fare_amount, rider_bonus_used when status = STARTED and driver matches. Does not modify distance_m. Returns rows affected.
func (r *TripRepo) UpdateToFinished(ctx context.Context, tripID string, driverUserID int64, fareAmount int64, riderBonusUsed int64) (rowsAffected int64, err error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE trips SET status = ?1, finished_at = datetime('now'), fare_amount = ?2, rider_bonus_used = ?3
		WHERE id = ?4 AND driver_user_id = ?5 AND status = ?6`,
		domain.TripStatusFinished, fareAmount, riderBonusUsed, tripID, driverUserID, domain.TripStatusStarted)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// AddTripDistance increments trips.distance_m by segmentMeters only when status = STARTED (ensures live accumulation for GET /trip/:id).
func (r *TripRepo) AddTripDistance(ctx context.Context, tripID string, segmentMeters int64) (rowsAffected int64, err error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE trips SET distance_m = distance_m + ?1 WHERE id = ?2 AND status = ?3`,
		segmentMeters, tripID, domain.TripStatusStarted)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// GetTripDistanceAndFare returns distance_m, fare_amount (nullable), and status for the trip. Returns sql.ErrNoRows if not found.
func (r *TripRepo) GetTripDistanceAndFare(ctx context.Context, tripID string) (distanceM int64, fareAmount sql.NullInt64, status string, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT distance_m, fare_amount, status FROM trips WHERE id = ?1`, tripID).
		Scan(&distanceM, &fareAmount, &status)
	return distanceM, fareAmount, status, err
}

// CancelByDriver sets status = CANCELLED_BY_DRIVER, cancelled_at, cancelled_by = "driver", optional cancel_reason.
func (r *TripRepo) CancelByDriver(ctx context.Context, tripID string, driverUserID int64, cancelReason *string) (rowsAffected int64, riderUserID int64, err error) {
	var reason interface{}
	if cancelReason != nil {
		reason = *cancelReason
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE trips SET status = ?1, cancelled_at = datetime('now'), cancelled_by = ?2, cancel_reason = ?3
		WHERE id = ?4 AND driver_user_id = ?5 AND status IN (?6, ?7, ?8)`,
		domain.TripStatusCancelledByDriver, "driver", reason, tripID, driverUserID, domain.TripStatusWaiting, domain.TripStatusArrived, domain.TripStatusStarted)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, 0, nil
	}
	_ = r.db.QueryRowContext(ctx, `SELECT rider_user_id FROM trips WHERE id = ?1`, tripID).Scan(&riderUserID)
	return n, riderUserID, nil
}

// CancelByRider sets status = CANCELLED_BY_RIDER, cancelled_at, cancelled_by = "rider", optional cancel_reason.
func (r *TripRepo) CancelByRider(ctx context.Context, tripID string, riderUserID int64, cancelReason *string) (rowsAffected int64, driverUserID int64, err error) {
	var reason interface{}
	if cancelReason != nil {
		reason = *cancelReason
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE trips SET status = ?1, cancelled_at = datetime('now'), cancelled_by = ?2, cancel_reason = ?3
		WHERE id = ?4 AND rider_user_id = ?5 AND status IN (?6, ?7, ?8)`,
		domain.TripStatusCancelledByRider, "rider", reason, tripID, riderUserID, domain.TripStatusWaiting, domain.TripStatusArrived, domain.TripStatusStarted)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, 0, nil
	}
	_ = r.db.QueryRowContext(ctx, `SELECT driver_user_id FROM trips WHERE id = ?1`, tripID).Scan(&driverUserID)
	return n, driverUserID, nil
}
