package legal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ActiveLegalFingerprint encodes active document_type:version pairs in stable order (driver_terms:2|privacy_policy:1|user_terms:2).
func ActiveLegalFingerprint(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT document_type, version FROM legal_documents WHERE is_active = 1 ORDER BY document_type`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var dt string
		var ver int
		if err := rows.Scan(&dt, &ver); err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s:%d", dt, ver))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(parts, "|"), nil
}

// ActiveLegalFingerprintLabels turns a fingerprint into readable "driver_terms v2, user_terms v2, ...".
func ActiveLegalFingerprintLabels(fp string) string {
	if fp == "" {
		return ""
	}
	segs := strings.Split(fp, "|")
	var out []string
	for _, s := range segs {
		parts := strings.SplitN(s, ":", 2)
		if len(parts) != 2 {
			continue
		}
		out = append(out, fmt.Sprintf("%s v%s", parts[0], parts[1]))
	}
	return strings.Join(out, ", ")
}

// SyncDriverLegalPromptFingerprint stores the current active fingerprint (call when driver is fully compliant).
func SyncDriverLegalPromptFingerprint(ctx context.Context, db *sql.DB, userID int64) error {
	fp, err := ActiveLegalFingerprint(ctx, db)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE drivers SET legal_terms_prompt_fingerprint = ?1 WHERE user_id = ?2`, fp, userID)
	return err
}
