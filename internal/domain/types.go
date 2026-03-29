package domain

// User roles (users.role).
const (
	RoleRider  = "rider"
	RoleDriver = "driver"
)

// Ride request status (ride_requests.status).
const (
	RequestStatusPending   = "PENDING"
	RequestStatusAssigned  = "ASSIGNED"
	RequestStatusCancelled = "CANCELLED"
	RequestStatusExpired   = "EXPIRED"
)

// Trip status (trips.status).
const (
	TripStatusWaiting         = "WAITING"
	TripStatusArrived         = "ARRIVED"
	TripStatusStarted         = "STARTED"
	TripStatusFinished        = "FINISHED"
	TripStatusCancelled       = "CANCELLED"
	TripStatusCancelledByDriver = "CANCELLED_BY_DRIVER"
	TripStatusCancelledByRider  = "CANCELLED_BY_RIDER"
)

// Notification status (request_notifications.status).
const (
	NotificationStatusSent     = "SENT"
	NotificationStatusAccepted = "ACCEPTED"
	NotificationStatusRejected = "REJECTED"
	NotificationStatusTimeout  = "TIMEOUT"
)
