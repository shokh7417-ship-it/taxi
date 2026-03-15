package models

// RideRequest represents a ride request row (subset of fields).
type RideRequest struct {
	ID                   string
	RiderUserID          int64
	PickupLat             float64
	PickupLng             float64
	RadiusKm              float64
	Status                string
	CreatedAt             string
	ExpiresAt             string
	AssignedDriverUserID  int64
	AssignedAt            string
}
