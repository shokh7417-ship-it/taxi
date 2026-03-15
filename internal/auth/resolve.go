package auth

import (
	"context"
	"database/sql"

	"taxi-mvp/internal/domain"
)

// ResolveUserFromTelegramID looks up internal user_id and role by Telegram user id.
// Returns (0, "", err) if not found or error.
func ResolveUserFromTelegramID(ctx context.Context, db *sql.DB, telegramUserID int64) (userID int64, role string, err error) {
	err = db.QueryRowContext(ctx,
		`SELECT id, role FROM users WHERE telegram_id = ?1`,
		telegramUserID).Scan(&userID, &role)
	if err != nil {
		return 0, "", err
	}
	return userID, role, nil
}

// ResolveDriverByUserID verifies that the given user_id exists and is a driver (has a row in drivers).
// Returns userID and nil, or 0 and error. Use for X-Driver-Id header auth when you trust the Mini App origin.
func ResolveDriverByUserID(ctx context.Context, db *sql.DB, userID int64) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT d.user_id FROM drivers d INNER JOIN users u ON u.id = d.user_id WHERE d.user_id = ?1`,
		userID).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, err
		}
		return 0, err
	}
	return id, nil
}

// AuthorizeTripAccess returns true if the user is the assigned driver or the rider of the trip.
// Used to allow WebSocket subscription and to enforce driver/rider actions on the correct trip.
func AuthorizeTripAccess(ctx context.Context, db *sql.DB, userID int64, tripID string, role string) (allowed bool, err error) {
	var driverUserID, riderUserID int64
	err = db.QueryRowContext(ctx,
		`SELECT driver_user_id, rider_user_id FROM trips WHERE id = ?1`,
		tripID).Scan(&driverUserID, &riderUserID)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	switch role {
	case domain.RoleDriver:
		return userID == driverUserID, nil
	case domain.RoleRider:
		return userID == riderUserID, nil
	default:
		return false, nil
	}
}
