package accounting

import "errors"

// ErrEmptyTripID is returned when promo/referral grant helpers require a non-empty trip id.
var ErrEmptyTripID = errors.New("trip_id is required for trip-finish grants")
