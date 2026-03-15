package service

import (
	"context"
	"database/sql"
	"math"
	"time"

	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/utils"
)

// AddPoint appends a location to trip_locations for the given trip.
// Call when driver sends location and trip status is STARTED.
func AddPoint(ctx context.Context, db *sql.DB, tripID string, lat, lng float64, ts time.Time) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO trip_locations (trip_id, lat, lng, ts) VALUES (?1, ?2, ?3, ?4)`,
		tripID, lat, lng, ts)
	return err
}

// FinishTrip sets trip status to FINISHED, computes distance_m from trip_locations
// (sum of Haversine between consecutive points), sets fare_amount = FareFromMeters(distance_m, pricePerKm).
func FinishTrip(ctx context.Context, db *sql.DB, tripID string, pricePerKm int64) error {
	rows, err := db.QueryContext(ctx, `
		SELECT lat, lng FROM trip_locations WHERE trip_id = ?1 ORDER BY ts ASC`,
		tripID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var lats, lngs []float64
	for rows.Next() {
		var lat, lng float64
		if err := rows.Scan(&lat, &lng); err != nil {
			return err
		}
		lats = append(lats, lat)
		lngs = append(lngs, lng)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var distanceM int64
	for i := 1; i < len(lats); i++ {
		distanceM += int64(math.Round(utils.HaversineMeters(lats[i-1], lngs[i-1], lats[i], lngs[i])))
	}
	fareAmount := utils.FareFromMeters(distanceM, pricePerKm)

	_, err = db.ExecContext(ctx, `
		UPDATE trips SET status = ?1, finished_at = ?2, distance_m = ?3, fare_amount = ?4
		WHERE id = ?5 AND status = ?6`,
		domain.TripStatusFinished, time.Now(), distanceM, fareAmount, tripID, domain.TripStatusStarted)
	return err
}
