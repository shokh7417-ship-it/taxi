package utils

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// ReferralLink returns the Telegram start link for a given bot username and referral code.
// Format: https://t.me/BotUsername?start=ref_CODE (e.g. https://t.me/YettiQanotTaxiBot?start=ref_ABC123).
func ReferralLink(botUsername, referralCode string) string {
	if botUsername == "" || referralCode == "" {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s?start=ref_%s", botUsername, referralCode)
}

const (
	referralCodeLength = 8   // 8 chars = 4 bytes hex = 32 bits
	referralCodeMaxTry = 100 // max attempts to generate unique code
)

// GenerateReferralCode returns a new unique referral code for the users table.
// Uses lowercase hex (0-9, a-f) for readability. Retries until unique or max attempts.
func GenerateReferralCode(ctx context.Context, db *sql.DB) (string, error) {
	for i := 0; i < referralCodeMaxTry; i++ {
		b := make([]byte, referralCodeLength/2)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		code := hex.EncodeToString(b)[:referralCodeLength]
		var exists int
		err := db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE referral_code = ?1`, code).Scan(&exists)
		if err == sql.ErrNoRows {
			return code, nil
		}
		if err != nil {
			return "", err
		}
		// Row exists with this code: try again
	}
	// Fallback: use longer code to reduce collision
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
