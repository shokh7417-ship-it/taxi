package models

// FareSettings holds admin-controlled tariff and commission (one row, id=1).
type FareSettings struct {
	ID               int64
	BaseFare         int64  // Start/base fare (so'm)
	Tier0_1Km        int64  // Price per km for 0–1 km (so'm)
	Tier1_2Km        int64  // Price per km for 1–2 km (so'm)
	Tier2PlusKm      int64  // Price per km for 2+ km (so'm)
	CommissionPercent int   // Percentage taken from fare (e.g. 5 or 10)
	UpdatedAt        string
	UpdatedBy        *int64
}
