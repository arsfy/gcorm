package runtime

import (
	"context"
	"database/sql"
)

// Executor is the minimal database execution interface.
// Both DB and Tx implement this interface.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Logger defines the interface for structured logging within the ORM.
type Logger interface {
	Log(ctx context.Context, level LogLevel, msg string, args ...any)
}

// LogLevel represents the severity of a log message.
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

// DB wraps a database connection with ORM-specific functionality.
type DB struct {
	executor Executor
	dialect  Dialect
	logger   Logger
	rawDB    *sql.DB
}

// NewDB creates a new DB instance from a *sql.DB connection.
func NewDB(rawDB *sql.DB, dialect Dialect, opts ...Option) *DB {
	db := &DB{
		executor: rawDB,
		dialect:  dialect,
		rawDB:    rawDB,
	}
	for _, opt := range opts {
		opt(db)
	}
	return db
}

// Option configures a DB instance.
type Option func(*DB)

// WithLogger sets the logger for the DB.
func WithLogger(l Logger) Option {
	return func(db *DB) {
		db.logger = l
	}
}

// Close closes the underlying database connection.
func (db *DB) Close() error {
	return db.rawDB.Close()
}

// Executor returns the underlying executor.
func (db *DB) Executor() Executor {
	return db.executor
}

// Dialect returns the database dialect.
func (db *DB) Dialect() Dialect {
	return db.dialect
}

// RawDB returns the underlying *sql.DB.
func (db *DB) RawDB() *sql.DB {
	return db.rawDB
}

// Tx executes a function within a database transaction.
// If the function returns an error, the transaction is rolled back.
// Otherwise, the transaction is committed.
func (db *DB) Tx(ctx context.Context, fn func(tx *Tx) error) error {
	return db.TxWithOptions(ctx, nil, fn)
}

// TxWithOptions executes a function within a transaction with specific options.
func (db *DB) TxWithOptions(ctx context.Context, opts *sql.TxOptions, fn func(tx *Tx) error) error {
	rawTx, err := db.rawDB.BeginTx(ctx, opts)
	if err != nil {
		return err
	}

	tx := &Tx{
		executor: rawTx,
		rawTx:    rawTx,
		dialect:  db.dialect,
		logger:   db.logger,
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}
