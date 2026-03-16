package models

// Driver is a minimal projection for admin balance views.
// It does not affect existing trip/dispatch/location logic.
type Driver struct {
	ID                 int64  `db:"id" json:"driver_id"`
	Name               string `db:"name" json:"name"`
	Phone              string `db:"phone" json:"phone"`
	CarModel           string `db:"car_model" json:"car_model"`
	PlateNumber        string `db:"plate_number" json:"plate_number"`
	Balance            int64  `db:"balance" json:"balance"`
	TotalPaid          int64  `db:"total_paid" json:"total_paid"`
	VerificationStatus string `db:"verification_status" json:"verification_status"` // pending, approved, rejected
}

