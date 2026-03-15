package repositories

import (
	"context"
	"database/sql"
	"time"
)

// TripStatsRepository provides read-only trip statistics for admin dashboards.
type TripStatsRepository interface {
	CountTripsForDay(ctx context.Context, day time.Time) (int64, error)
}

type tripStatsRepo struct {
	db *sql.DB
}

// NewTripStatsRepository returns a TripStatsRepository backed by *sql.DB.
func NewTripStatsRepository(db *sql.DB) TripStatsRepository {
	return &tripStatsRepo{db: db}
}

// CountTripsForDay counts trips whose started_at timestamp falls within [day, day+24h).
// Uses existing trips table and does not change trip logic.
func (r *tripStatsRepo) CountTripsForDay(ctx context.Context, day time.Time) (int64, error) {
	dayStart := day.Truncate(24 * time.Hour).UTC()
	dayEnd := dayStart.Add(24 * time.Hour)
	var count int64
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM trips
		WHERE started_at IS NOT NULL
		  AND started_at >= ?1
		  AND started_at < ?2`,
		dayStart.Format("2006-01-02 15:04:05"), dayEnd.Format("2006-01-02 15:04:05"),
	).Scan(&count)
	return count, err
}

