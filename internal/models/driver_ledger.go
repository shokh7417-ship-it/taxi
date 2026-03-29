package models

import "time"

// Driver ledger entry types (append-only audit). Amount is signed: positive credits the driver bucket, negative debits.
const (
	LedgerEntryPromoGranted              = "PROMO_GRANTED"
	LedgerEntryCommissionAccrued         = "COMMISSION_ACCRUED"
	LedgerEntryPromoAppliedToCommission  = "PROMO_APPLIED_TO_COMMISSION"
	LedgerEntryCashAppliedToCommission   = "CASH_APPLIED_TO_COMMISSION"
	LedgerEntryPromoExpired              = "PROMO_EXPIRED"
	LedgerEntryManualAdjustment          = "MANUAL_ADJUSTMENT"
	LedgerEntryCashTopUp                 = "CASH_TOPUP"
	LedgerEntryCashDeduction             = "CASH_DEDUCTION"
)

const LedgerBucketPromo = "promo"
const LedgerBucketCash  = "cash"

// DriverLedgerEntry is one append-only row in driver_ledger.
type DriverLedgerEntry struct {
	ID            int64     `json:"id"`
	DriverID      int64     `json:"driver_id"`
	Bucket        string    `json:"bucket"`
	EntryType     string    `json:"entry_type"`
	Amount        int64     `json:"amount"`
	ReferenceType *string   `json:"reference_type,omitempty"`
	ReferenceID   *string   `json:"reference_id,omitempty"`
	Note          *string   `json:"note,omitempty"`
	MetadataJSON  *string   `json:"metadata_json,omitempty"`
	ExpiresAt     *string   `json:"expires_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}
