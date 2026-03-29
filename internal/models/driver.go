package models

// Driver is a minimal projection for admin balance views.
// It does not affect existing trip/dispatch/location logic.
// Balance is promo_balance+cash_balance (denormalized for dispatch/eligibility queries).
type Driver struct {
	ID                 int64  `db:"id" json:"driver_id"`
	Name               string `db:"name" json:"name"`
	Phone              string `db:"phone" json:"phone"`
	CarModel           string `db:"car_model" json:"car_model"`
	PlateNumber        string `db:"plate_number" json:"plate_number"`
	PromoBalance       int64  `db:"promo_balance" json:"promo_balance"`
	CashBalance        int64  `db:"cash_balance" json:"cash_balance"`
	Balance            int64  `db:"balance" json:"balance"` // promo_balance + cash_balance
	TotalPaid          int64  `db:"total_paid" json:"total_paid"`
	VerificationStatus string `db:"verification_status" json:"verification_status"` // pending, approved, rejected
	// Scanned from admin list query (1 = acceptance matches an active document version).
	HasDriverTerms int `db:"has_driver_terms"`
	HasUserTerms   int `db:"has_user_terms"`
	HasPrivacy     int `db:"has_privacy"`
}

