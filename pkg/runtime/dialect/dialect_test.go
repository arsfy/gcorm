package dialect

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/arsfy/gcorm/pkg/runtime"
	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
)

// ---------------------------------------------------------------------------
// All three dialects implement runtime.Dialect (compile-time + runtime)
// ---------------------------------------------------------------------------

func TestDialectsImplementInterface(t *testing.T) {
	dialects := []runtime.Dialect{PostgreSQL{}, MySQL{}, SQLite{}}
	for _, d := range dialects {
		if d.Name() == "" {
			t.Error("Name() should not be empty")
		}
	}
}

// ---------------------------------------------------------------------------
// Placeholder output
// ---------------------------------------------------------------------------

func TestPlaceholder(t *testing.T) {
	cases := []struct {
		dialect runtime.Dialect
		n       int
		want    string
	}{
		{PostgreSQL{}, 1, "$1"},
		{PostgreSQL{}, 5, "$5"},
		{MySQL{}, 1, "?"},
		{MySQL{}, 99, "?"},
		{SQLite{}, 1, "?"},
		{SQLite{}, 42, "?"},
	}
	for _, tc := range cases {
		got := tc.dialect.Placeholder(tc.n)
		if got != tc.want {
			t.Errorf("%s.Placeholder(%d) = %q, want %q", tc.dialect.Name(), tc.n, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// QuoteIdent — single and multi-part identifiers
// ---------------------------------------------------------------------------

func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		dialect runtime.Dialect
		parts   []string
		want    string
	}{
		{PostgreSQL{}, []string{"users"}, `"users"`},
		{PostgreSQL{}, []string{"public", "users"}, `"public"."users"`},
		{MySQL{}, []string{"users"}, "`users`"},
		{MySQL{}, []string{"mydb", "users"}, "`mydb`.`users`"},
		{SQLite{}, []string{"users"}, `"users"`},
		{SQLite{}, []string{"main", "items"}, `"main"."items"`},
	}
	for _, tc := range cases {
		got := tc.dialect.QuoteIdent(tc.parts...)
		if got != tc.want {
			t.Errorf("%s.QuoteIdent(%v) = %q, want %q", tc.dialect.Name(), tc.parts, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// SupportsReturning
// ---------------------------------------------------------------------------

func TestSupportsReturning(t *testing.T) {
	cases := []struct {
		dialect runtime.Dialect
		want    bool
	}{
		{PostgreSQL{}, true},
		{MySQL{}, false},
		{SQLite{}, false},
	}
	for _, tc := range cases {
		if got := tc.dialect.SupportsReturning(); got != tc.want {
			t.Errorf("%s.SupportsReturning() = %v, want %v", tc.dialect.Name(), got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// SupportsSchemas
// ---------------------------------------------------------------------------

func TestSupportsSchemas(t *testing.T) {
	cases := []struct {
		dialect runtime.Dialect
		want    bool
	}{
		{PostgreSQL{}, true},
		{MySQL{}, true},
		{SQLite{}, false},
	}
	for _, tc := range cases {
		if got := tc.dialect.SupportsSchemas(); got != tc.want {
			t.Errorf("%s.SupportsSchemas() = %v, want %v", tc.dialect.Name(), got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TypeMapping for all schema types on all dialects
// ---------------------------------------------------------------------------

func TestTypeMapping(t *testing.T) {
	types := []struct {
		schemaType string
		pg         string
		mysql      string
		sqlite     string
	}{
		{"String", "TEXT", "VARCHAR(191)", "TEXT"},
		{"Int", "INTEGER", "INTEGER", "INTEGER"},
		{"BigInt", "BIGINT", "BIGINT", "INTEGER"},
		{"Float", "DOUBLE PRECISION", "DOUBLE", "REAL"},
		{"Decimal", "DECIMAL", "DECIMAL", "TEXT"},
		{"Boolean", "BOOLEAN", "BOOLEAN", "INTEGER"},
		{"DateTime", "TIMESTAMPTZ", "DATETIME(3)", "TEXT"},
		{"Bytes", "BYTEA", "LONGBLOB", "BLOB"},
		{"Json", "JSONB", "JSON", "TEXT"},
		{"UUID", "UUID", "VARCHAR(36)", "TEXT"},
	}

	pg := PostgreSQL{}
	my := MySQL{}
	sl := SQLite{}

	for _, tc := range types {
		t.Run(tc.schemaType, func(t *testing.T) {
			if got := pg.TypeMapping(tc.schemaType, false); got != tc.pg {
				t.Errorf("PostgreSQL.TypeMapping(%q) = %q, want %q", tc.schemaType, got, tc.pg)
			}
			if got := my.TypeMapping(tc.schemaType, false); got != tc.mysql {
				t.Errorf("MySQL.TypeMapping(%q) = %q, want %q", tc.schemaType, got, tc.mysql)
			}
			if got := sl.TypeMapping(tc.schemaType, false); got != tc.sqlite {
				t.Errorf("SQLite.TypeMapping(%q) = %q, want %q", tc.schemaType, got, tc.sqlite)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DefaultValueExpression
// ---------------------------------------------------------------------------

func TestDefaultValueExpression(t *testing.T) {
	pg := PostgreSQL{}
	my := MySQL{}
	sl := SQLite{}

	// uuid()
	if got := pg.DefaultValueExpression("uuid", nil); got != "gen_random_uuid()" {
		t.Errorf("pg uuid = %q", got)
	}
	if got := my.DefaultValueExpression("uuid", nil); got != "(UUID())" {
		t.Errorf("mysql uuid = %q", got)
	}
	if got := sl.DefaultValueExpression("uuid", nil); got == "" {
		t.Error("sqlite uuid should not be empty")
	}

	// now()
	if got := pg.DefaultValueExpression("now", nil); got != "NOW()" {
		t.Errorf("pg now = %q", got)
	}
	if got := my.DefaultValueExpression("now", nil); got != "NOW(3)" {
		t.Errorf("mysql now = %q", got)
	}
	if got := sl.DefaultValueExpression("now", nil); got != "(datetime('now'))" {
		t.Errorf("sqlite now = %q", got)
	}

	// autoincrement()
	if got := pg.DefaultValueExpression("autoincrement", nil); got != "GENERATED BY DEFAULT AS IDENTITY" {
		t.Errorf("pg autoincrement = %q", got)
	}
	if got := my.DefaultValueExpression("autoincrement", nil); got != "AUTO_INCREMENT" {
		t.Errorf("mysql autoincrement = %q", got)
	}
	if got := sl.DefaultValueExpression("autoincrement", nil); got != "AUTOINCREMENT" {
		t.Errorf("sqlite autoincrement = %q", got)
	}

	// pg sequence
	if got := pg.DefaultValueExpression("sequence", []string{"my_seq"}); got != "nextval('my_seq')" {
		t.Errorf("pg sequence = %q", got)
	}
}

// ---------------------------------------------------------------------------
// SupportsFeature
// ---------------------------------------------------------------------------

func TestSupportsFeature(t *testing.T) {
	type featureCase struct {
		feature runtime.Feature
		pg      bool
		mysql   bool
		sqlite  bool
	}

	cases := []featureCase{
		{runtime.FeatureReturning, true, false, false},
		{runtime.FeatureSchemas, true, true, false},
		{runtime.FeatureJSON, true, true, false},
		{runtime.FeatureUUID, true, false, false},
		{runtime.FeatureArrays, true, false, false},
		{runtime.FeatureEnumType, true, false, false},
		{runtime.FeatureCascadeDelete, true, true, true},
		{runtime.FeatureSerialType, true, true, false},
		{runtime.FeatureIdentityColumn, true, false, false},
		{runtime.FeatureUpsert, true, true, true},
		{runtime.FeaturePartialIndex, true, false, false},
	}

	pg := PostgreSQL{}
	my := MySQL{}
	sl := SQLite{}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("Feature_%d", tc.feature), func(t *testing.T) {
			if got := pg.SupportsFeature(tc.feature); got != tc.pg {
				t.Errorf("PostgreSQL.SupportsFeature(%d) = %v, want %v", tc.feature, got, tc.pg)
			}
			if got := my.SupportsFeature(tc.feature); got != tc.mysql {
				t.Errorf("MySQL.SupportsFeature(%d) = %v, want %v", tc.feature, got, tc.mysql)
			}
			if got := sl.SupportsFeature(tc.feature); got != tc.sqlite {
				t.Errorf("SQLite.SupportsFeature(%d) = %v, want %v", tc.feature, got, tc.sqlite)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RewriteLimitOffset
// ---------------------------------------------------------------------------

func TestRewriteLimitOffset(t *testing.T) {
	intPtr := func(n int) *int { return &n }

	cases := []struct {
		name    string
		dialect runtime.Dialect
		limit   *int
		offset  *int
		want    string
	}{
		{"pg limit only", PostgreSQL{}, intPtr(10), nil, "LIMIT 10"},
		{"pg offset only", PostgreSQL{}, nil, intPtr(20), "OFFSET 20"},
		{"pg both", PostgreSQL{}, intPtr(10), intPtr(20), "LIMIT 10 OFFSET 20"},
		{"pg neither", PostgreSQL{}, nil, nil, ""},
		{"mysql limit only", MySQL{}, intPtr(5), nil, "LIMIT 5"},
		{"mysql both", MySQL{}, intPtr(5), intPtr(10), "LIMIT 5 OFFSET 10"},
		{"sqlite limit only", SQLite{}, intPtr(5), nil, "LIMIT 5"},
		{"sqlite offset only", SQLite{}, nil, intPtr(10), "LIMIT -1 OFFSET 10"},
		{"sqlite both", SQLite{}, intPtr(5), intPtr(10), "LIMIT 5 OFFSET 10"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.dialect.RewriteLimitOffset(tc.limit, tc.offset)
			if got != tc.want {
				t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ClassifyError — nil input
// ---------------------------------------------------------------------------

func TestClassifyErrorNil(t *testing.T) {
	dialects := []runtime.Dialect{PostgreSQL{}, MySQL{}, SQLite{}}
	for _, d := range dialects {
		if got := d.ClassifyError(nil); got != nil {
			t.Errorf("%s.ClassifyError(nil) should return nil, got %+v", d.Name(), got)
		}
	}
}

// ---------------------------------------------------------------------------
// ClassifyError — known patterns (basic smoke tests)
// ---------------------------------------------------------------------------

func TestClassifyErrorPatterns(t *testing.T) {
	// PostgreSQL unique violation
	pgErr := PostgreSQL{}.ClassifyError(fmt.Errorf(`pq: ERROR: duplicate key violates unique constraint "users_email_key" (SQLSTATE 23505)`))
	if pgErr == nil || pgErr.Code != gcoerr.CodeUniqueViolation {
		t.Errorf("PostgreSQL unique violation: got %+v", pgErr)
	}

	// MySQL duplicate entry
	myErr := MySQL{}.ClassifyError(fmt.Errorf("Error 1062: Duplicate entry 'alice' for key 'users.email'"))
	if myErr == nil || myErr.Code != gcoerr.CodeUniqueViolation {
		t.Errorf("MySQL duplicate entry: got %+v", myErr)
	}

	// SQLite unique constraint
	slErr := SQLite{}.ClassifyError(fmt.Errorf("UNIQUE constraint failed: users.email"))
	if slErr == nil || slErr.Code != gcoerr.CodeUniqueViolation {
		t.Errorf("SQLite unique constraint: got %+v", slErr)
	}
}

// ---------------------------------------------------------------------------
// ClassifyError — comprehensive PostgreSQL tests
// ---------------------------------------------------------------------------

func TestPostgreSQLClassifyError(t *testing.T) {
	pg := PostgreSQL{}

	tests := []struct {
		name string
		err  error
		code gcoerr.ErrorCode
		meta map[string]string
	}{
		{
			name: "unique violation with constraint name",
			err:  fmt.Errorf(`pq: duplicate key value violates unique constraint "users_email_key"`),
			code: gcoerr.CodeUniqueViolation,
			meta: map[string]string{"constraint": "users_email_key"},
		},
		{
			name: "unique violation by SQLSTATE",
			err:  fmt.Errorf("ERROR: SQLSTATE 23505"),
			code: gcoerr.CodeUniqueViolation,
		},
		{
			name: "foreign key violation",
			err:  fmt.Errorf(`pq: insert or update on table "posts" violates foreign key constraint "posts_author_id_fkey"`),
			code: gcoerr.CodeForeignKeyViolation,
			meta: map[string]string{"constraint": "posts_author_id_fkey"},
		},
		{
			name: "foreign key violation by SQLSTATE",
			err:  fmt.Errorf("ERROR: SQLSTATE 23503"),
			code: gcoerr.CodeForeignKeyViolation,
		},
		{
			name: "not-null violation",
			err:  fmt.Errorf("pq: null value in column \"name\" violates not-null constraint"),
			code: gcoerr.CodeNotNullViolation,
		},
		{
			name: "not-null violation by SQLSTATE",
			err:  fmt.Errorf("ERROR: SQLSTATE 23502"),
			code: gcoerr.CodeNotNullViolation,
		},
		{
			name: "check violation",
			err:  fmt.Errorf(`pq: new row for relation "users" violates check_violation (SQLSTATE 23514)`),
			code: gcoerr.CodeCheckViolation,
		},
		{
			name: "serialization failure",
			err:  fmt.Errorf("pq: could not serialize access (SQLSTATE 40001)"),
			code: gcoerr.CodeSerializationFailure,
		},
		{
			name: "deadlock",
			err:  fmt.Errorf("pq: deadlock detected (SQLSTATE 40P01)"),
			code: gcoerr.CodeDeadlock,
		},
		{
			name: "timeout - canceling statement",
			err:  fmt.Errorf("pq: canceling statement due to statement timeout"),
			code: gcoerr.CodeTimeout,
		},
		{
			name: "timeout by SQLSTATE",
			err:  fmt.Errorf("pq: ERROR (SQLSTATE 57014)"),
			code: gcoerr.CodeTimeout,
		},
		{
			name: "data truncation",
			err:  fmt.Errorf("pq: value too long for type (SQLSTATE 22001)"),
			code: gcoerr.CodeDataTruncation,
		},
		{
			name: "connection refused",
			err:  fmt.Errorf("dial tcp 127.0.0.1:5432: connection refused"),
			code: gcoerr.CodeConnectionError,
		},
		{
			name: "connection error by SQLSTATE 08006",
			err:  fmt.Errorf("pq: connection failure (SQLSTATE 08006)"),
			code: gcoerr.CodeConnectionError,
		},
		{
			name: "not found via sql.ErrNoRows",
			err:  sql.ErrNoRows,
			code: gcoerr.CodeNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dbErr := pg.ClassifyError(tc.err)
			if dbErr == nil {
				t.Fatalf("expected non-nil DBError for %q", tc.err)
			}
			if dbErr.Code != tc.code {
				t.Errorf("Code = %v, want %v", dbErr.Code, tc.code)
			}
			if dbErr.Cause != tc.err {
				t.Errorf("Cause should wrap original error")
			}
			for k, v := range tc.meta {
				if dbErr.Meta[k] != v {
					t.Errorf("Meta[%q] = %q, want %q", k, dbErr.Meta[k], v)
				}
			}
		})
	}

	// Unknown error returns nil
	if pg.ClassifyError(fmt.Errorf("some random error")) != nil {
		t.Error("expected nil for unrecognised error")
	}
}

// ---------------------------------------------------------------------------
// ClassifyError — comprehensive MySQL tests
// ---------------------------------------------------------------------------

func TestMySQLClassifyError(t *testing.T) {
	my := MySQL{}

	tests := []struct {
		name string
		err  error
		code gcoerr.ErrorCode
		meta map[string]string
	}{
		{
			name: "duplicate entry",
			err:  fmt.Errorf("Error 1062: Duplicate entry 'alice' for key 'users.email'"),
			code: gcoerr.CodeUniqueViolation,
			meta: map[string]string{"constraint": "users.email"},
		},
		{
			name: "foreign key - cannot add child",
			err:  fmt.Errorf("Error 1452: Cannot add or update a child row: a foreign key constraint fails"),
			code: gcoerr.CodeForeignKeyViolation,
		},
		{
			name: "foreign key - cannot delete parent",
			err:  fmt.Errorf("Error 1451: Cannot delete or update a parent row: a foreign key constraint fails"),
			code: gcoerr.CodeForeignKeyViolation,
		},
		{
			name: "not-null violation",
			err:  fmt.Errorf("Error 1048: Column 'name' cannot be null"),
			code: gcoerr.CodeNotNullViolation,
		},
		{
			name: "check constraint",
			err:  fmt.Errorf("Error 1644: Check constraint violated"),
			code: gcoerr.CodeCheckViolation,
		},
		{
			name: "deadlock",
			err:  fmt.Errorf("Error 1213: Deadlock found when trying to get lock"),
			code: gcoerr.CodeDeadlock,
		},
		{
			name: "lock wait timeout",
			err:  fmt.Errorf("Error 1205: Lock wait timeout exceeded"),
			code: gcoerr.CodeTimeout,
		},
		{
			name: "data too long",
			err:  fmt.Errorf("Error 1406: Data too long for column 'name'"),
			code: gcoerr.CodeDataTruncation,
		},
		{
			name: "connection error 2002",
			err:  fmt.Errorf("Error 2002: Can't connect to local MySQL server"),
			code: gcoerr.CodeConnectionError,
		},
		{
			name: "connection error 2003",
			err:  fmt.Errorf("Error 2003: Can't connect to MySQL server on '127.0.0.1'"),
			code: gcoerr.CodeConnectionError,
		},
		{
			name: "connection error 2006",
			err:  fmt.Errorf("Error 2006: MySQL server has gone away"),
			code: gcoerr.CodeConnectionError,
		},
		{
			name: "not found via sql.ErrNoRows",
			err:  sql.ErrNoRows,
			code: gcoerr.CodeNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dbErr := my.ClassifyError(tc.err)
			if dbErr == nil {
				t.Fatalf("expected non-nil DBError for %q", tc.err)
			}
			if dbErr.Code != tc.code {
				t.Errorf("Code = %v, want %v", dbErr.Code, tc.code)
			}
			if dbErr.Cause != tc.err {
				t.Errorf("Cause should wrap original error")
			}
			for k, v := range tc.meta {
				if dbErr.Meta[k] != v {
					t.Errorf("Meta[%q] = %q, want %q", k, dbErr.Meta[k], v)
				}
			}
		})
	}

	// Unknown error returns nil
	if my.ClassifyError(fmt.Errorf("some random error")) != nil {
		t.Error("expected nil for unrecognised error")
	}
}

// ---------------------------------------------------------------------------
// ClassifyError — comprehensive SQLite tests
// ---------------------------------------------------------------------------

func TestSQLiteClassifyError(t *testing.T) {
	sl := SQLite{}

	tests := []struct {
		name string
		err  error
		code gcoerr.ErrorCode
		meta map[string]string
	}{
		{
			name: "unique constraint failed",
			err:  fmt.Errorf("UNIQUE constraint failed: users.email"),
			code: gcoerr.CodeUniqueViolation,
			meta: map[string]string{"constraint": "users.email"},
		},
		{
			name: "foreign key constraint failed",
			err:  fmt.Errorf("FOREIGN KEY constraint failed"),
			code: gcoerr.CodeForeignKeyViolation,
		},
		{
			name: "not-null constraint failed",
			err:  fmt.Errorf("NOT NULL constraint failed: users.name"),
			code: gcoerr.CodeNotNullViolation,
		},
		{
			name: "check constraint failed",
			err:  fmt.Errorf("CHECK constraint failed: age_positive"),
			code: gcoerr.CodeCheckViolation,
		},
		{
			name: "database is locked",
			err:  fmt.Errorf("database is locked"),
			code: gcoerr.CodeDeadlock,
		},
		{
			name: "database table is locked",
			err:  fmt.Errorf("database table is locked"),
			code: gcoerr.CodeDeadlock,
		},
		{
			name: "disk i/o error",
			err:  fmt.Errorf("disk i/o error"),
			code: gcoerr.CodeConnectionError,
		},
		{
			name: "unable to open database",
			err:  fmt.Errorf("unable to open database file"),
			code: gcoerr.CodeConnectionError,
		},
		{
			name: "not found via sql.ErrNoRows",
			err:  sql.ErrNoRows,
			code: gcoerr.CodeNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dbErr := sl.ClassifyError(tc.err)
			if dbErr == nil {
				t.Fatalf("expected non-nil DBError for %q", tc.err)
			}
			if dbErr.Code != tc.code {
				t.Errorf("Code = %v, want %v", dbErr.Code, tc.code)
			}
			if dbErr.Cause != tc.err {
				t.Errorf("Cause should wrap original error")
			}
			for k, v := range tc.meta {
				if dbErr.Meta[k] != v {
					t.Errorf("Meta[%q] = %q, want %q", k, dbErr.Meta[k], v)
				}
			}
		})
	}

	// Unknown error returns nil
	if sl.ClassifyError(fmt.Errorf("some random error")) != nil {
		t.Error("expected nil for unrecognised error")
	}
}

// ---------------------------------------------------------------------------
// Name values
// ---------------------------------------------------------------------------

func TestDialectNames(t *testing.T) {
	pg := PostgreSQL{}
	my := MySQL{}
	sl := SQLite{}

	if pg.Name() != "postgresql" {
		t.Error("PostgreSQL name wrong")
	}
	if my.Name() != "mysql" {
		t.Error("MySQL name wrong")
	}
	if sl.Name() != "sqlite" {
		t.Error("SQLite name wrong")
	}
}
