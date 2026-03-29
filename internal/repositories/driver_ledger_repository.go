package repositories

import (
	"context"
	"database/sql"
	"time"

	"taxi-mvp/internal/models"
)

// DriverLedgerRepository appends rows to driver_ledger (no updates/deletes).
type DriverLedgerRepository struct {
	db *sql.DB
}

func NewDriverLedgerRepository(db *sql.DB) *DriverLedgerRepository {
	return &DriverLedgerRepository{db: db}
}

// InsertTx appends a ledger row inside an existing transaction.
func (r *DriverLedgerRepository) InsertTx(ctx context.Context, tx *sql.Tx, e *models.DriverLedgerEntry) error {
	var refType, refID, note, meta, exp interface{}
	if e.ReferenceType != nil {
		refType = *e.ReferenceType
	}
	if e.ReferenceID != nil {
		refID = *e.ReferenceID
	}
	if e.Note != nil {
		note = *e.Note
	}
	if e.MetadataJSON != nil {
		meta = *e.MetadataJSON
	}
	if e.ExpiresAt != nil {
		exp = *e.ExpiresAt
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO driver_ledger (driver_id, bucket, entry_type, amount, reference_type, reference_id, note, metadata_json, expires_at)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)`,
		e.DriverID, e.Bucket, e.EntryType, e.Amount, refType, refID, note, meta, exp)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	e.ID = id
	e.CreatedAt = time.Now().UTC()
	return nil
}

// InsertTxOrIgnore inserts a ledger row; returns inserted=true if a new row was written.
// Requires a UNIQUE constraint on (driver_id, reference_type, reference_id) for idempotent grants.
func (r *DriverLedgerRepository) InsertTxOrIgnore(ctx context.Context, tx *sql.Tx, e *models.DriverLedgerEntry) (inserted bool, err error) {
	var refType, refID, note, meta, exp interface{}
	if e.ReferenceType != nil {
		refType = *e.ReferenceType
	}
	if e.ReferenceID != nil {
		refID = *e.ReferenceID
	}
	if e.Note != nil {
		note = *e.Note
	}
	if e.MetadataJSON != nil {
		meta = *e.MetadataJSON
	}
	if e.ExpiresAt != nil {
		exp = *e.ExpiresAt
	}
	res, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO driver_ledger (driver_id, bucket, entry_type, amount, reference_type, reference_id, note, metadata_json, expires_at)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)`,
		e.DriverID, e.Bucket, e.EntryType, e.Amount, refType, refID, note, meta, exp)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		id, err := res.LastInsertId()
		if err == nil && id > 0 {
			e.ID = id
		}
		e.CreatedAt = time.Now().UTC()
	}
	return n > 0, nil
}

// ListByDriver returns recent ledger rows for a driver (newest first).
func (r *DriverLedgerRepository) ListByDriver(ctx context.Context, driverID int64, limit int) ([]models.DriverLedgerEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, driver_id, bucket, entry_type, amount, reference_type, reference_id, note, metadata_json, expires_at, created_at
		FROM driver_ledger WHERE driver_id = ?1 ORDER BY id DESC LIMIT ?2`, driverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DriverLedgerEntry
	for rows.Next() {
		var e models.DriverLedgerEntry
		var refType, refID, note, meta, exp sql.NullString
		var created string
		if err := rows.Scan(&e.ID, &e.DriverID, &e.Bucket, &e.EntryType, &e.Amount, &refType, &refID, &note, &meta, &exp, &created); err != nil {
			return nil, err
		}
		if refType.Valid {
			s := refType.String
			e.ReferenceType = &s
		}
		if refID.Valid {
			s := refID.String
			e.ReferenceID = &s
		}
		if note.Valid {
			s := note.String
			e.Note = &s
		}
		if meta.Valid {
			s := meta.String
			e.MetadataJSON = &s
		}
		if exp.Valid {
			s := exp.String
			e.ExpiresAt = &s
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", created, time.UTC); err == nil {
			e.CreatedAt = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
