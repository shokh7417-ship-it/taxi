package domain

import "errors"

// Domain errors for trip state machine and handlers.
var (
	ErrInvalidTransition = errors.New("invalid trip state transition")
	ErrTripNotFound      = errors.New("trip not found")
	ErrAlreadyFinished   = errors.New("trip already finished")
	ErrAlreadyCancelled  = errors.New("trip already cancelled")
	ErrNoOp              = errors.New("noop") // not used as error; noop is returned as success with result "noop"
	// Pickup proximity / live location (trip start / mark arrived).
	ErrTooFarFromPickup     = errors.New("too far from pickup")
	ErrDriverLocationStale  = errors.New("driver live location stale or missing")
	ErrLiveLocationInactive = errors.New("telegram live location not active")
)

// allowedTransitions defines valid (from -> to) trip status changes.
// Terminal states (FINISHED, CANCELLED_*) have no outgoing transitions.
var allowedTransitions = map[string]map[string]bool{
	TripStatusWaiting: {
		TripStatusArrived:           true,
		TripStatusStarted:           true,
		TripStatusCancelledByDriver: true,
		TripStatusCancelledByRider:  true,
	},
	TripStatusArrived: {
		TripStatusStarted:           true,
		TripStatusCancelledByDriver: true,
		TripStatusCancelledByRider:  true,
	},
	TripStatusStarted: {
		TripStatusFinished:          true,
		TripStatusCancelledByDriver: true,
		TripStatusCancelledByRider:  true,
	},
	// FINISHED and CANCELLED_* have no outgoing transitions
}

// CanTransition reports whether transitioning from status "from" to "to" is allowed.
func CanTransition(from, to string) bool {
	if from == "" || to == "" {
		return false
	}
	outs, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	return outs[to]
}

// ValidateTransition returns ErrInvalidTransition if the transition is not allowed.
func ValidateTransition(from, to string) error {
	if CanTransition(from, to) {
		return nil
	}
	return ErrInvalidTransition
}

// IsTerminal returns true for FINISHED and any CANCELLED_* status.
func IsTerminal(status string) bool {
	return status == TripStatusFinished ||
		status == TripStatusCancelledByDriver ||
		status == TripStatusCancelledByRider ||
		status == TripStatusCancelled
}
