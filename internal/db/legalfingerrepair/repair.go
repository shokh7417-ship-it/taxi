// Package legalfingerrepair adds optional drivers columns when missing
// (e.g. goose version advanced without applying migrations on this database).
package legalfingerrepair

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

func ensureColumn(ctx context.Context, db *sql.DB, columnName, alterSQL string) error {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('drivers') WHERE name = ?1`, columnName).Scan(&n)
	if err != nil {
		return fmt.Errorf("legalfingerrepair: pragma drivers %s: %w", columnName, err)
	}
	if n > 0 {
		return nil
	}
	log.Printf("legalfingerrepair: adding drivers.%s", columnName)
	if _, err := db.ExecContext(ctx, alterSQL); err != nil {
		return fmt.Errorf("legalfingerrepair: add %s: %w", columnName, err)
	}
	return nil
}

// Ensure adds legal_terms_prompt_fingerprint and application_admin_sent to drivers if absent.
func Ensure(ctx context.Context, db *sql.DB) error {
	if err := ensureColumn(ctx, db, "legal_terms_prompt_fingerprint", `ALTER TABLE drivers ADD COLUMN legal_terms_prompt_fingerprint TEXT`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "application_admin_sent", `ALTER TABLE drivers ADD COLUMN application_admin_sent INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	return nil
}
