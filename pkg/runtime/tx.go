package runtime

import (
	"context"
	"database/sql"
	"errors"
)

// Tx wraps a database transaction with ORM-specific functionality.
type Tx struct {
	executor Executor
	rawTx    *sql.Tx
	dialect  Dialect
	logger   Logger
	done     bool
}

// Executor returns the transaction executor.
func (tx *Tx) Executor() Executor {
	return tx.executor
}

// Dialect returns the database dialect.
func (tx *Tx) Dialect() Dialect {
	return tx.dialect
}

// ExecContext executes a query within the transaction without returning rows.
func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.executor.ExecContext(ctx, query, args...)
}

// QueryContext executes a query within the transaction that returns rows.
func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.executor.QueryContext(ctx, query, args...)
}

// QueryRowContext executes a query within the transaction expected to return at most one row.
func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.executor.QueryRowContext(ctx, query, args...)
}

// Commit commits the transaction.
func (tx *Tx) Commit() error {
	if tx.done {
		return errors.New("gco: transaction already completed")
	}
	tx.done = true
	return tx.rawTx.Commit()
}

// Rollback rolls back the transaction.
func (tx *Tx) Rollback() error {
	if tx.done {
		return errors.New("gco: transaction already completed")
	}
	tx.done = true
	return tx.rawTx.Rollback()
}

var _ Executor = (*Tx)(nil)
