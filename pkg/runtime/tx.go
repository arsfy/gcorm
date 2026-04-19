package runtime

import (
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
