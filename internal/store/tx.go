package store

import (
	"context"
	"database/sql"
)

// dbtx abstracts *sql.DB and *sql.Tx so query methods work with either.
type dbtx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
