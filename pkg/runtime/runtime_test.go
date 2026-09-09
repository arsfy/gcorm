package runtime

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
	_ "modernc.org/sqlite"
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

func assertExecutorDelegates(t *testing.T, exec Executor, mock *mockExecutor) {
	t.Helper()

	ctx := context.Background()
	wantRow := new(sql.Row)
	execCalled := false
	queryCalled := false
	queryRowCalled := false

	mock.execFn = func(gotCtx context.Context, query string, args ...any) (sql.Result, error) {
		execCalled = true
		if gotCtx != ctx || query != "EXEC" || !reflect.DeepEqual(args, []any{1, "two"}) {
			t.Errorf("ExecContext delegation = (%v, %q, %#v)", gotCtx, query, args)
		}
		return fakeResult{}, nil
	}
	mock.queryFn = func(gotCtx context.Context, query string, args ...any) (*sql.Rows, error) {
		queryCalled = true
		if gotCtx != ctx || query != "QUERY" || !reflect.DeepEqual(args, []any{3}) {
			t.Errorf("QueryContext delegation = (%v, %q, %#v)", gotCtx, query, args)
		}
		return nil, nil
	}
	mock.queryRowFn = func(gotCtx context.Context, query string, args ...any) *sql.Row {
		queryRowCalled = true
		if gotCtx != ctx || query != "ROW" || !reflect.DeepEqual(args, []any{4}) {
			t.Errorf("QueryRowContext delegation = (%v, %q, %#v)", gotCtx, query, args)
		}
		return wantRow
	}

	result, err := exec.ExecContext(ctx, "EXEC", 1, "two")
	if err != nil {
		t.Fatalf("ExecContext() error = %v", err)
	}
	if _, ok := result.(fakeResult); !ok {
		t.Fatalf("ExecContext() result = %T, want fakeResult", result)
	}
	rows, err := exec.QueryContext(ctx, "QUERY", 3)
	if err != nil || rows != nil {
		t.Fatalf("QueryContext() = (%v, %v), want (nil, nil)", rows, err)
	}
	if row := exec.QueryRowContext(ctx, "ROW", 4); row != wantRow {
		t.Fatalf("QueryRowContext() = %p, want %p", row, wantRow)
	}
	if !execCalled || !queryCalled || !queryRowCalled {
		t.Fatalf("delegate calls: exec=%v query=%v queryRow=%v", execCalled, queryCalled, queryRowCalled)
	}
}

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

func TestDBImplementsAndDelegatesExecutor(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	mock := &mockExecutor{}
	db := NewDB(rawDB, &mockDialect{name: "test"})
	db.executor = mock

	assertExecutorDelegates(t, db, mock)
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

func TestTxImplementsAndDelegatesExecutor(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	db := NewDB(rawDB, &mockDialect{name: "test"})
	err := db.Tx(context.Background(), func(tx *Tx) error {
		mock := &mockExecutor{}
		tx.executor = mock
		assertExecutorDelegates(t, tx, mock)
		return nil
	})
	if err != nil {
		t.Fatalf("Tx() error = %v", err)
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

func TestTxCallbackCompletedTransaction(t *testing.T) {
	sentinel := errors.New("callback error")

	tests := []struct {
		name    string
		run     func(t *testing.T, tx *Tx) error
		wantErr error
	}{
		{
			name: "commit inside callback then return nil",
			run: func(t *testing.T, tx *Tx) error {
				if err := tx.Commit(); err != nil {
					t.Errorf("callback Commit() error = %v", err)
				}
				return nil
			},
			wantErr: nil,
		},
		{
			name: "rollback inside callback then return nil",
			run: func(t *testing.T, tx *Tx) error {
				if err := tx.Rollback(); err != nil {
					t.Errorf("callback Rollback() error = %v", err)
				}
				return nil
			},
			wantErr: nil,
		},
		{
			name: "commit inside callback then return error",
			run: func(t *testing.T, tx *Tx) error {
				if err := tx.Commit(); err != nil {
					t.Errorf("callback Commit() error = %v", err)
				}
				return sentinel
			},
			wantErr: sentinel,
		},
		{
			name: "rollback inside callback then return error",
			run: func(t *testing.T, tx *Tx) error {
				if err := tx.Rollback(); err != nil {
					t.Errorf("callback Rollback() error = %v", err)
				}
				return sentinel
			},
			wantErr: sentinel,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rawDB := openFakeDB(t)
			defer rawDB.Close()

			db := NewDB(rawDB, &mockDialect{name: "test"})
			err := db.Tx(context.Background(), func(tx *Tx) error {
				return tc.run(t, tx)
			})

			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Tx() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestTxPanicAfterCallbackCommit(t *testing.T) {
	rawDB := openFakeDB(t)
	defer rawDB.Close()

	db := NewDB(rawDB, &mockDialect{name: "test"})

	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic to be re-raised")
		}
		if r != "test panic" {
			t.Errorf("expected panic value %q, got %v", "test panic", r)
		}
	}()

	_ = db.Tx(context.Background(), func(tx *Tx) error {
		if err := tx.Commit(); err != nil {
			t.Errorf("callback Commit() error = %v", err)
		}
		panic("test panic")
	})
}

func TestTxCallbackCommitPersistsSQLite(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "tx.db")
	rawDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()

	if _, err := rawDB.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatal(err)
	}

	db := NewDB(rawDB, &mockDialect{name: "sqlite"})
	err = db.Tx(context.Background(), func(tx *Tx) error {
		if _, err := tx.ExecContext(context.Background(), "INSERT INTO items (name) VALUES (?)", "widget"); err != nil {
			return err
		}
		// The callback commits the transaction itself; Tx must not report
		// "transaction already completed" for the already-committed tx.
		return tx.Commit()
	})
	if err != nil {
		t.Fatalf("Tx() error = %v, want nil", err)
	}

	var count int
	if err := rawDB.QueryRow("SELECT COUNT(*) FROM items").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (callback commit should persist)", count)
	}
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
