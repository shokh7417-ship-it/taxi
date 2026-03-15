package models

// Trip represents a trip row (subset of fields used by services/handlers).
type Trip struct {
	ID             string
	RequestID      string
	DriverUserID   int64
	RiderUserID   int64
	Status         string
	StartedAt      string
	FinishedAt     string
	DistanceM      int64
	FareAmount     int64
}
