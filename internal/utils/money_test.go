package utils

import (
	"testing"
)

func TestCalculateFareRounded(t *testing.T) {
	baseFare := 5000.0
	perKmFare := 2000.0

	tests := []struct {
		name       string
		baseFare   float64
		perKmFare  float64
		distanceKm float64
		want       int64
	}{
		{"zero distance", baseFare, perKmFare, 0, 5000},
		{"one km", baseFare, perKmFare, 1, 7000},
		{"half km", baseFare, perKmFare, 0.5, 6000},
		{"0.25 rounds down", baseFare, perKmFare, 0.25, 5500},
		{"0.5 rounds up", baseFare, perKmFare, 0.5, 6000},
		{"2.4 km", baseFare, perKmFare, 2.4, 9800},
		{"negative distance treated as 0", baseFare, perKmFare, -1, 5000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateFareRounded(tt.baseFare, tt.perKmFare, tt.distanceKm)
			if got != tt.want {
				t.Errorf("CalculateFareRounded(%v, %v, %v) = %d, want %d",
					tt.baseFare, tt.perKmFare, tt.distanceKm, got, tt.want)
			}
		})
	}
}

func TestCalculateFareTiered(t *testing.T) {
	base := 4000.0
	tier0_1 := 1500.0
	tier1_2 := 1200.0
	tier2Plus := 1000.0
	tests := []struct {
		name       string
		baseFare   float64
		t0_1       float64
		t1_2       float64
		t2Plus     float64
		distanceKm float64
		want       int64
	}{
		{"0 km", base, tier0_1, tier1_2, tier2Plus, 0, 4000},
		{"0.5 km => base + 0.5*tier0_1", base, tier0_1, tier1_2, tier2Plus, 0.5, 4750},   // 4000 + 750
		{"1 km => base + 1*tier0_1", base, tier0_1, tier1_2, tier2Plus, 1, 5500},         // 4000 + 1500
		{"1.6 km => base + 1*tier0_1 + 0.6*tier1_2", base, tier0_1, tier1_2, tier2Plus, 1.6, 6220}, // 4000+1500+720
		{"3.2 km => base + t0_1 + t1_2 + 1.2*t2Plus", base, tier0_1, tier1_2, tier2Plus, 3.2, 7900}, // 4000+1500+1200+1200
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateFareTiered(tt.baseFare, tt.t0_1, tt.t1_2, tt.t2Plus, tt.distanceKm)
			if got != tt.want {
				t.Errorf("CalculateFareTiered(...) = %d, want %d", got, tt.want)
			}
		})
	}
}
