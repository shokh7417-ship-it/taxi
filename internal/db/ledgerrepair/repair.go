// Package ledgerrepair fixes common driver_ledger schema drift (e.g. user_id vs driver_id)
// and rebuilds the table when it exists but is incompatible (so deploy does not fatally exit).
package ledgerrepair

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

const createDriverLedgerSQL = `
CREATE TABLE IF NOT EXISTS driver_ledger (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  driver_id INTEGER NOT NULL REFERENCES drivers(user_id) ON DELETE CASCADE,
  bucket TEXT NOT NULL CHECK (bucket IN ('promo','cash')),
  entry_type TEXT NOT NULL,
  amount INTEGER NOT NULL,
  reference_type TEXT,
  reference_id TEXT,
  note TEXT,
  metadata_json TEXT,
  expires_at TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

const createDriverLedgerIndexSQL = `CREATE INDEX IF NOT EXISTS idx_driver_ledger_driver_created ON driver_ledger(driver_id, created_at DESC)`

// tableExists reports whether a table named name exists (SQLite).
func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?1`, name).Scan(&n)
	return n > 0, err
}

func columnSet(ctx context.Context, db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?1)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

func createCanonicalDriverLedger(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createDriverLedgerSQL); err != nil {
		return fmt.Errorf("ledgerrepair: create driver_ledger: %w", err)
	}
	if _, err := db.ExecContext(ctx, createDriverLedgerIndexSQL); err != nil {
		return fmt.Errorf("ledgerrepair: create driver_ledger index: %w", err)
	}
	return nil
}

// Ensure aligns driver_ledger with application code. Creates the table if missing.
func Ensure(ctx context.Context, db *sql.DB) error {
	ok, err := tableExists(ctx, db, "driver_ledger")
	if err != nil {
		return err
	}
	if !ok {
		log.Printf("ledgerrepair: driver_ledger missing; creating canonical table")
		return createCanonicalDriverLedger(ctx, db)
	}

	cols, err := columnSet(ctx, db, "driver_ledger")
	if err != nil {
		return err
	}
	if _, has := cols["driver_id"]; has {
		return nil
	}
	if _, has := cols["user_id"]; has {
		log.Printf("ledgerrepair: driver_ledger column user_id → driver_id")
		if _, err := db.ExecContext(ctx, `ALTER TABLE driver_ledger RENAME COLUMN user_id TO driver_id`); err != nil {
			return fmt.Errorf("ledgerrepair: rename user_id to driver_id: %w", err)
		}
		return nil
	}
	if _, has := cols["driver_user_id"]; has {
		log.Printf("ledgerrepair: driver_ledger column driver_user_id → driver_id")
		if _, err := db.ExecContext(ctx, `ALTER TABLE driver_ledger RENAME COLUMN driver_user_id TO driver_id`); err != nil {
			return fmt.Errorf("ledgerrepair: rename driver_user_id to driver_id: %w", err)
		}
		return nil
	}

	backup := fmt.Sprintf("driver_ledger_legacy_%s", time.Now().UTC().Format("20060102_150405"))
	log.Printf("ledgerrepair: driver_ledger has no driver key column; renaming to %s and creating canonical table", backup)
	if _, err := db.ExecContext(ctx, "ALTER TABLE driver_ledger RENAME TO "+backup); err != nil {
		log.Printf("ledgerrepair: rename to backup failed (%v); dropping driver_ledger", err)
		if _, err2 := db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_driver_ledger_driver_created`); err2 != nil {
			return fmt.Errorf("ledgerrepair: drop index: %w", err2)
		}
		if _, err2 := db.ExecContext(ctx, `DROP TABLE driver_ledger`); err2 != nil {
			return fmt.Errorf("ledgerrepair: drop driver_ledger: %w", err2)
		}
	}
	return createCanonicalDriverLedger(ctx, db)
}
