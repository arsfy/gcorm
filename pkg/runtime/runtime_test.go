package runtime

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
)

// ---------------------------------------------------------------------------
// Fake database driver (standard library only, no external dependencies)
// ---------------------------------------------------------------------------

type fakeDriver struct{}

func (d *fakeDriver) Open(_ string) (driver.Conn, error) {
	return &fakeConn{}, nil
}

type fakeConn struct{}

func (c *fakeConn) Prepare(_ string) (driver.Stmt, error) { return &fakeStmt{}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return &fakeTx{}, nil }

type fakeTx struct {
	committed  bool
	rolledBack bool
}

func (t *fakeTx) Commit() error   { t.committed = true; return nil }
func (t *fakeTx) Rollback() error { t.rolledBack = true; return nil }

type fakeStmt struct{}

func (s *fakeStmt) Close() error                                 { return nil }
func (s *fakeStmt) NumInput() int                                { return 0 }
func (s *fakeStmt) Exec(_ []driver.Value) (driver.Result, error) { return fakeResult{}, nil }
func (s *fakeStmt) Query(_ []driver.Value) (driver.Rows, error)  { return &fakeRows{}, nil }

type fakeResult struct{}

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return 0, nil }

type fakeRows struct{}

func (r *fakeRows) Columns() []string           { return nil }
func (r *fakeRows) Close() error                { return nil }
func (r *fakeRows) Next(_ []driver.Value) error { return io.EOF }

func init() {
	sql.Register("fakedb", &fakeDriver{})
}

func openFakeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("fakedb", "")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// ---------------------------------------------------------------------------
// Mock executor
// ---------------------------------------------------------------------------

type mockExecutor struct {
	execFn     func(ctx context.Context, query string, args ...any) (sql.Result, error)
	queryFn    func(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	queryRowFn func(ctx context.Context, query string, args ...any) *sql.Row
}

func (m *mockExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if m.execFn != nil {
		return m.execFn(ctx, query, args...)
	}
	return nil, nil
}

func (m *mockExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, query, args...)
	}
	return nil, nil
}

func (m *mockExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if m.queryRowFn != nil {
		return m.queryRowFn(ctx, query, args...)
	}
	return nil
}

// Compile-time check: mockExecutor satisfies Executor.
var _ Executor = (*mockExecutor)(nil)

// ---------------------------------------------------------------------------
// Mock dialect
// ---------------------------------------------------------------------------

type mockDialect struct {
	name     string
	features map[Feature]bool
}

func (d *mockDialect) Name() string                                       { return d.name }
func (d *mockDialect) Placeholder(n int) string                           { return "?" }
func (d *mockDialect) QuoteIdent(parts ...string) string                  { return "" }
func (d *mockDialect) SupportsReturning() bool                            { return false }
func (d *mockDialect) SupportsSchemas() bool                              { return false }
func (d *mockDialect) RewriteLimitOffset(_, _ *int) string                { return "" }
func (d *mockDialect) ClassifyError(_ error) *DBError                     { return nil }
func (d *mockDialect) TypeMapping(_ string, _ bool) string                { return "" }
func (d *mockDialect) DefaultValueExpression(_ string, _ []string) string { return "" }
func (d *mockDialect) SupportsFeature(f Feature) bool                     { return d.features[f] }

// Compile-time check: mockDialect satisfies Dialect.
var _ Dialect = (*mockDialect)(nil)

// ---------------------------------------------------------------------------
// Mock logger
// ---------------------------------------------------------------------------

type mockLogger struct {
	called bool
	last   string
}

func (l *mockLogger) Log(_ context.Context, _ LogLevel, msg string, _ ...any) {
	l.called = true
	l.last = msg
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNewDB(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	dialect := &mockDialect{name: "test"}
	db := NewDB(rawDB, dialect)

	if db == nil {
		t.Fatal("NewDB returned nil")
	}
	if db.RawDB() != rawDB {
		t.Error("RawDB() does not return the original *sql.DB")
	}
	if db.Dialect() != dialect {
		t.Error("Dialect() does not return the configured dialect")
	}
	if db.Executor() == nil {
		t.Error("Executor() should not be nil")
	}
}

func TestDBExposesDialectAndExecutor(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	dialect := &mockDialect{name: "postgresql"}
	db := NewDB(rawDB, dialect)

	if db.Dialect().Name() != "postgresql" {
		t.Errorf("expected dialect name %q, got %q", "postgresql", db.Dialect().Name())
	}

	// The default executor should be the raw *sql.DB itself.
	if db.Executor() != Executor(rawDB) {
		t.Error("default executor should be the raw *sql.DB")
	}
}

func TestWithLoggerOption(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	logger := &mockLogger{}
	dialect := &mockDialect{name: "test"}
	db := NewDB(rawDB, dialect, WithLogger(logger))

	if db.logger != logger {
		t.Error("WithLogger option did not set the logger")
	}
}

func TestDBClose(t *testing.T) {
	rawDB := openFakeDB(t)
	dialect := &mockDialect{name: "test"}
	db := NewDB(rawDB, dialect)

	if err := db.Close(); err != nil {
		t.Errorf("Close() returned unexpected error: %v", err)
	}
}

func TestTxCommitOnSuccess(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	dialect := &mockDialect{name: "test"}
	db := NewDB(rawDB, dialect)

	executed := false
	err := db.Tx(context.Background(), func(tx *Tx) error {
		executed = true
		if tx.Executor() == nil {
			t.Error("Tx.Executor() should not be nil")
		}
		if tx.Dialect() != dialect {
			t.Error("Tx.Dialect() should return the DB dialect")
		}
		return nil
	})

	if err != nil {
		t.Errorf("Tx() returned unexpected error: %v", err)
	}
	if !executed {
		t.Error("transaction function was not executed")
	}
}

func TestTxRollbackOnError(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	dialect := &mockDialect{name: "test"}
	db := NewDB(rawDB, dialect)

	expectedErr := errors.New("forced error")
	err := db.Tx(context.Background(), func(_ *Tx) error {
		return expectedErr
	})

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestTxRollbackOnPanic(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	dialect := &mockDialect{name: "test"}
	db := NewDB(rawDB, dialect)

	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic to be re-raised")
		}
		if r != "test panic" {
			t.Errorf("expected panic value %q, got %v", "test panic", r)
		}
	}()

	_ = db.Tx(context.Background(), func(_ *Tx) error {
		panic("test panic")
	})
}

func TestTxDoubleCommitReturnsError(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	dialect := &mockDialect{name: "test"}
	db := NewDB(rawDB, dialect)

	var capturedTx *Tx
	_ = db.Tx(context.Background(), func(tx *Tx) error {
		capturedTx = tx
		return nil
	})

	// Transaction was already committed by Tx(); second commit should fail.
	err := capturedTx.Commit()
	if err == nil {
		t.Error("expected error on double commit")
	}
}

func TestTxWithOptions(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	dialect := &mockDialect{name: "test"}
	db := NewDB(rawDB, dialect)

	opts := &sql.TxOptions{
		ReadOnly: false,
	}

	err := db.TxWithOptions(context.Background(), opts, func(tx *Tx) error {
		return nil
	})

	if err != nil {
		t.Errorf("TxWithOptions() returned unexpected error: %v", err)
	}
}

func TestFeatureEnumValuesDistinct(t *testing.T) {
	features := []Feature{
		FeatureReturning,
		FeatureSchemas,
		FeatureJSON,
		FeatureUUID,
		FeatureArrays,
		FeatureEnumType,
		FeatureCascadeDelete,
		FeatureSerialType,
		FeatureIdentityColumn,
		FeatureUpsert,
		FeaturePartialIndex,
	}

	seen := make(map[Feature]bool, len(features))
	for _, f := range features {
		if seen[f] {
			t.Errorf("duplicate Feature value: %d", f)
		}
		seen[f] = true
	}
}

func TestDBErrorInterface(t *testing.T) {
	inner := errors.New("connection refused")
	dbErr := &DBError{
		Code:    gcoerr.CodeConnectionError,
		Message: "connection error",
		Cause:   inner,
	}

	// Verify error message includes code and message.
	got := dbErr.Error()
	if !strings.Contains(got, "connection error") {
		t.Errorf("expected message to contain 'connection error', got %q", got)
	}

	// Verify Unwrap returns the inner error.
	if !errors.Is(dbErr, inner) {
		t.Error("errors.Is should find the inner error via Unwrap")
	}

	// Verify DBError without inner error.
	dbErr2 := &DBError{Code: gcoerr.CodeUnknown, Message: "ok"}
	got2 := dbErr2.Error()
	if !strings.Contains(got2, "ok") {
		t.Errorf("expected message to contain 'ok', got %q", got2)
	}
}

func TestMockExecutorSatisfiesInterface(t *testing.T) {
	called := false
	mock := &mockExecutor{
		execFn: func(_ context.Context, query string, _ ...any) (sql.Result, error) {
			called = true
			if query != "SELECT 1" {
				t.Errorf("unexpected query: %s", query)
			}
			return nil, nil
		},
	}

	var exec Executor = mock
	_, _ = exec.ExecContext(context.Background(), "SELECT 1")
	if !called {
		t.Error("execFn was not invoked")
	}
}
