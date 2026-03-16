package models

// AdminDriverDTO is the admin-facing view of a driver with balance.
type AdminDriverDTO struct {
	DriverID           int64  `json:"driver_id"`
	Name               string `json:"name"`
	Phone              string `json:"phone"`
	CarModel           string `json:"car_model"`
	PlateNumber        string `json:"plate_number"`
	Balance            int64  `json:"balance"`
	TotalPaid          int64  `json:"total_paid"`
	Status             string `json:"status"`             // "ACTIVE" or "INACTIVE"
	VerificationStatus string `json:"verification_status"` // pending, approved, rejected
}

