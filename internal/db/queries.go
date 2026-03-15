package db

import "database/sql"

// Ping checks the database connection is alive.
func Ping(db *sql.DB) error {
	return db.Ping()
}
