package db

import (
	"database/sql"
	"fmt"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite" // enables DATABASE_URL=file:... local SQLite with libsql driver
)

// Open connects to Turso (libSQL), pings it, and returns the DB.
// databaseURL must be a libsql URL, e.g. libsql://your-db.turso.io?authToken=...
func Open(databaseURL string) (*sql.DB, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL or TURSO_DATABASE_URL + TURSO_AUTH_TOKEN required")
	}
	db, err := sql.Open("libsql", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("sql open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}

// Close closes the database connection.
func Close(db *sql.DB) error {
	if db == nil {
		return nil
	}
	return db.Close()
}
