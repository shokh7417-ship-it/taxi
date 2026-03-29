package accounting

import (
	"context"
	"database/sql"
)

// DBTX is implemented by *sql.DB and *sql.Tx for reads/writes in a single transaction.
type DBTX interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}
