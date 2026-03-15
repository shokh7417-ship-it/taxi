package domain

import (
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{"WAITING->STARTED", TripStatusWaiting, TripStatusStarted, true},
		{"WAITING->CANCELLED_BY_DRIVER", TripStatusWaiting, TripStatusCancelledByDriver, true},
		{"WAITING->CANCELLED_BY_RIDER", TripStatusWaiting, TripStatusCancelledByRider, true},
		{"STARTED->FINISHED", TripStatusStarted, TripStatusFinished, true},
		{"STARTED->CANCELLED_BY_DRIVER", TripStatusStarted, TripStatusCancelledByDriver, true},
		{"STARTED->CANCELLED_BY_RIDER", TripStatusStarted, TripStatusCancelledByRider, true},
		{"WAITING->FINISHED disallowed", TripStatusWaiting, TripStatusFinished, false},
		{"STARTED->STARTED disallowed", TripStatusStarted, TripStatusStarted, false},
		{"FINISHED->anything disallowed", TripStatusFinished, TripStatusStarted, false},
		{"FINISHED->FINISHED disallowed", TripStatusFinished, TripStatusFinished, false},
		{"CANCELLED_BY_DRIVER->STARTED disallowed", TripStatusCancelledByDriver, TripStatusStarted, false},
		{"CANCELLED_BY_RIDER->FINISHED disallowed", TripStatusCancelledByRider, TripStatusFinished, false},
		{"empty from", "", TripStatusStarted, false},
		{"empty to", TripStatusWaiting, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanTransition(tt.from, tt.to)
			if got != tt.want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestValidateTransition(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want error
	}{
		{"valid WAITING->STARTED", TripStatusWaiting, TripStatusStarted, nil},
		{"valid STARTED->FINISHED", TripStatusStarted, TripStatusFinished, nil},
		{"invalid WAITING->FINISHED", TripStatusWaiting, TripStatusFinished, ErrInvalidTransition},
		{"invalid FINISHED->STARTED", TripStatusFinished, TripStatusStarted, ErrInvalidTransition},
		{"invalid STARTED->STARTED", TripStatusStarted, TripStatusStarted, ErrInvalidTransition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateTransition(tt.from, tt.to)
			if !errors.Is(got, tt.want) {
				t.Errorf("ValidateTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{TripStatusFinished, true},
		{TripStatusCancelledByDriver, true},
		{TripStatusCancelledByRider, true},
		{TripStatusCancelled, true},
		{TripStatusWaiting, false},
		{TripStatusStarted, false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := IsTerminal(tt.status)
			if got != tt.want {
				t.Errorf("IsTerminal(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}
