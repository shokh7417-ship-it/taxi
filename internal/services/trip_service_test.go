package services

import (
	"testing"
	"time"
)

func TestParseTripLocationTime(t *testing.T) {
	ref := time.Date(2026, 3, 11, 12, 30, 0, 0, time.UTC)

	tests := []struct {
		name     string
		v        interface{}
		wantZero bool
		wantUnix int64 // if !wantZero, check Unix() match (0 = skip)
	}{
		{"nil", nil, true, 0},
		{"string SQLite format", "2026-03-11 12:30:00", false, ref.Unix()},
		{"string RFC3339", "2026-03-11T12:30:00Z", false, ref.Unix()},
		{"[]byte", []byte("2026-03-11 12:30:00"), false, ref.Unix()},
		{"int64 unix seconds", ref.Unix(), false, ref.Unix()},
		{"int64 unix ms", ref.UnixMilli(), false, ref.Unix()},
		{"float64 unix", float64(ref.Unix()), false, ref.Unix()},
		{"time.Time", ref, false, ref.Unix()},
		{"empty string", "", true, 0},
		{"invalid string", "not-a-date", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTripLocationTime(tt.v)
			if got.IsZero() != tt.wantZero {
				t.Errorf("parseTripLocationTime(%v) IsZero = %v, want %v", tt.v, got.IsZero(), tt.wantZero)
			}
			if !tt.wantZero && tt.wantUnix != 0 && got.Unix() != tt.wantUnix {
				t.Errorf("parseTripLocationTime(%v) Unix = %d, want %d", tt.v, got.Unix(), tt.wantUnix)
			}
		})
	}
}
