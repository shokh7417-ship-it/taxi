package handlers

import (
	"testing"
)

func TestIgnoreReasonAccuracy(t *testing.T) {
	tests := []struct {
		name     string
		accuracy float64
		want     string
	}{
		{"zero accuracy accepted", 0, ""},
		{"50 meters accepted", 50, ""},
		{"49 meters accepted", 49, ""},
		{"51 meters ignored", 51, "accuracy too low"},
		{"100 meters ignored", 100, "accuracy too low"},
		{"negative accepted", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IgnoreReasonAccuracy(tt.accuracy)
			if got != tt.want {
				t.Errorf("IgnoreReasonAccuracy(%v) = %q, want %q", tt.accuracy, got, tt.want)
			}
		})
	}
}
