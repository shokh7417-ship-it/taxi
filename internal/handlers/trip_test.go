package handlers

import (
	"database/sql"
	"testing"
)

func TestTripFareForResponse(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		computedFare  int64 // fare from tiered/legacy for non-FINISHED
		fareAmount    sql.NullInt64
		wantFare      int64
		wantPtr       bool // true if fareAmount pointer should be non-nil
	}{
		{
			name:         "STARTED zero distance returns base fare, no stored amount",
			status:       "STARTED",
			computedFare: 4000,
			fareAmount:   sql.NullInt64{Valid: false},
			wantFare:     4000,
			wantPtr:      false,
		},
		{
			name:         "STARTED with distance returns estimated fare, no stored amount",
			status:       "STARTED",
			computedFare: 5500, // 4000 + 1*1500
			fareAmount:   sql.NullInt64{Valid: false},
			wantFare:     5500,
			wantPtr:      false,
		},
		{
			name:         "FINISHED with stored fare returns stored amount",
			status:       "FINISHED",
			computedFare: 4642,
			fareAmount:   sql.NullInt64{Int64: 4642, Valid: true},
			wantFare:     4642,
			wantPtr:      true,
		},
		{
			name:         "FINISHED with valid 0 still returns pointer",
			status:       "FINISHED",
			computedFare: 4000,
			fareAmount:   sql.NullInt64{Int64: 4000, Valid: true},
			wantFare:     4000,
			wantPtr:      true,
		},
		{
			name:         "WAITING uses estimated fare, no stored amount",
			status:       "WAITING",
			computedFare: 4000,
			fareAmount:   sql.NullInt64{Valid: false},
			wantFare:     4000,
			wantPtr:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fare, ptr := TripFareForResponse(tt.status, tt.fareAmount, tt.computedFare)
			if fare != tt.wantFare {
				t.Errorf("fare = %d, want %d", fare, tt.wantFare)
			}
			if (ptr != nil) != tt.wantPtr {
				t.Errorf("fareAmount ptr is nil = %v, wantPtr = %v", ptr == nil, tt.wantPtr)
			}
			if tt.wantPtr && ptr != nil && *ptr != tt.wantFare {
				t.Errorf("fareAmount *ptr = %d, want %d", *ptr, tt.wantFare)
			}
		})
	}
}
