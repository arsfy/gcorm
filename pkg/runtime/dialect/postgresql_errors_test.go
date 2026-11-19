package dialect

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
)

// libpqStyleError mimics lib/pq's *pq.Error: a string-kinded Code field and
// an Error() that does not include the SQLSTATE.
type libpqStyleCode string

type libpqStyleError struct {
	Code       libpqStyleCode
	Message    string
	Table      string
	Column     string
	Constraint string
}

func (e *libpqStyleError) Error() string { return "pq: " + e.Message }

func TestPostgreSQLClassifyErrorUsesPgErrorSQLState(t *testing.T) {
	tests := []struct {
		name       string
		err        *pgconn.PgError
		code       gcoerr.ErrorCode
		constraint string
	}{
		{"unique", &pgconn.PgError{Code: "23505", Message: `duplicate key value violates unique constraint "users_email_key"`, ConstraintName: "users_email_key", TableName: "users"}, gcoerr.CodeUniqueViolation, "users_email_key"},
		{"check named like unique", &pgconn.PgError{Code: "23514", Message: `new row for relation "t" violates check constraint "unique_violation_chk"`, ConstraintName: "unique_violation_chk"}, gcoerr.CodeCheckViolation, "unique_violation_chk"},
		{"exclusion", &pgconn.PgError{Code: "23P01", Message: `conflicting key value violates exclusion constraint "no_overlap"`, ConstraintName: "no_overlap"}, gcoerr.CodeExclusionViolation, "no_overlap"},
		{"foreign key", &pgconn.PgError{Code: "23503", ConstraintName: "posts_author_fkey"}, gcoerr.CodeForeignKeyViolation, "posts_author_fkey"},
		{"not null", &pgconn.PgError{Code: "23502", ColumnName: "name"}, gcoerr.CodeNotNullViolation, ""},
		{"truncation", &pgconn.PgError{Code: "22001"}, gcoerr.CodeDataTruncation, ""},
		{"numeric out of range", &pgconn.PgError{Code: "22003", Message: "integer out of range"}, gcoerr.CodeNumericOutOfRange, ""},
		{"invalid text representation", &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type integer: "x"`}, gcoerr.CodeInvalidTextRepresentation, ""},
		{"serialization", &pgconn.PgError{Code: "40001"}, gcoerr.CodeSerializationFailure, ""},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, gcoerr.CodeDeadlock, ""},
		{"query canceled", &pgconn.PgError{Code: "57014"}, gcoerr.CodeTimeout, ""},
		{"too many connections", &pgconn.PgError{Code: "53300"}, gcoerr.CodeConnectionError, ""},
		{"connection class 08", &pgconn.PgError{Code: "08006"}, gcoerr.CodeConnectionError, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("insert user: %w", tc.err)
			got := PostgreSQL{}.ClassifyError(wrapped)
			if got == nil {
				t.Fatalf("ClassifyError(%v) = nil", wrapped)
			}
			if got.Code != tc.code {
				t.Fatalf("Code = %v, want %v", got.Code, tc.code)
			}
			if got.Meta["sqlstate"] != tc.err.Code {
				t.Fatalf("Meta[sqlstate] = %q, want %q", got.Meta["sqlstate"], tc.err.Code)
			}
			if got.Meta["constraint"] != tc.constraint {
				t.Fatalf("Meta[constraint] = %q, want %q", got.Meta["constraint"], tc.constraint)
			}
			if !errors.Is(got, tc.err) {
				t.Fatalf("classified error must wrap the driver error")
			}
		})
	}
}

func TestPostgreSQLClassifyErrorUnmappedSQLStateIgnoresMessage(t *testing.T) {
	// 42P01 (undefined_table) is not classified even though the text mentions
	// a duplicate key; the SQLSTATE is authoritative.
	err := &pgconn.PgError{Code: "42P01", Message: "duplicate key value violates unique constraint"}
	if got := (PostgreSQL{}).ClassifyError(err); got != nil {
		t.Fatalf("ClassifyError = %+v, want nil", got)
	}
}

func TestPostgreSQLClassifyErrorLibpqStyleCodeField(t *testing.T) {
	err := &libpqStyleError{
		Code:       "23514",
		Message:    `new row for relation "t" violates check constraint "unique_violation_chk"`,
		Table:      "t",
		Constraint: "unique_violation_chk",
	}
	got := PostgreSQL{}.ClassifyError(fmt.Errorf("wrapped: %w", err))
	if got == nil || got.Code != gcoerr.CodeCheckViolation {
		t.Fatalf("ClassifyError = %+v, want CheckViolation", got)
	}
	if got.Meta["constraint"] != "unique_violation_chk" || got.Meta["table"] != "t" {
		t.Fatalf("Meta = %v", got.Meta)
	}

	exclusion := &libpqStyleError{Code: "23P01", Constraint: "no_overlap"}
	if got := (PostgreSQL{}).ClassifyError(exclusion); got == nil || got.Code != gcoerr.CodeExclusionViolation {
		t.Fatalf("exclusion ClassifyError = %+v", got)
	}
}

func TestPostgreSQLClassifyErrorJoinedErrors(t *testing.T) {
	err := errors.Join(errors.New("rollback failed"), &libpqStyleError{Code: "40P01"})
	if got := (PostgreSQL{}).ClassifyError(err); got == nil || got.Code != gcoerr.CodeDeadlock {
		t.Fatalf("ClassifyError = %+v, want Deadlock", got)
	}
}

func TestPostgreSQLClassifyErrorMessageFallback(t *testing.T) {
	tests := []struct {
		name       string
		msg        string
		code       gcoerr.ErrorCode
		constraint string
	}{
		{"lib/pq check without code", `pq: new row for relation "t" violates check constraint "unique_violation_chk"`, gcoerr.CodeCheckViolation, "unique_violation_chk"},
		{"lib/pq check named like fk", `pq: new row for relation "foreign key constraint" violates check constraint "duplicate key value"`, gcoerr.CodeCheckViolation, "duplicate key value"},
		{"lib/pq unique", `pq: duplicate key value violates unique constraint "users_email_key"`, gcoerr.CodeUniqueViolation, "users_email_key"},
		{"lib/pq exclusion", `pq: conflicting key value violates exclusion constraint "no_overlap"`, gcoerr.CodeExclusionViolation, "no_overlap"},
		{"lib/pq out of range", `pq: value "99999999999" is out of range for type integer`, gcoerr.CodeNumericOutOfRange, ""},
		{"lib/pq invalid input", `pq: invalid input syntax for type uuid: "abc"`, gcoerr.CodeInvalidTextRepresentation, ""},
		{"lib/pq too many clients", `pq: sorry, too many clients already`, gcoerr.CodeConnectionError, ""},
		{"pgx text with SQLSTATE beats words", `ERROR: new row for relation "t" violates check constraint "unique_violation_chk" (SQLSTATE 23514)`, gcoerr.CodeCheckViolation, "unique_violation_chk"},
		{"pgx exclusion text", `ERROR: conflicting key value violates exclusion constraint "x" (SQLSTATE 23P01)`, gcoerr.CodeExclusionViolation, "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PostgreSQL{}.ClassifyError(errors.New(tc.msg))
			if got == nil {
				t.Fatalf("ClassifyError(%q) = nil", tc.msg)
			}
			if got.Code != tc.code {
				t.Fatalf("Code = %v, want %v", got.Code, tc.code)
			}
			if got.Meta["constraint"] != tc.constraint {
				t.Fatalf("Meta[constraint] = %q, want %q", got.Meta["constraint"], tc.constraint)
			}
		})
	}
}

func TestPostgreSQLClassifyErrorNoFalsePositivesFromIdentifiers(t *testing.T) {
	for _, msg := range []string{
		`pq: relation "unique_violation" does not exist`,
		`pq: column "deadlock detected" does not exist`,
		`ERROR: relation "x (SQLSTATE 23505)" does not exist`,
		`runtime error: index out of range [3] with length 2`,
	} {
		if got := (PostgreSQL{}).ClassifyError(errors.New(msg)); got != nil {
			t.Errorf("ClassifyError(%q) = %v, want nil", msg, got.Code)
		}
	}
}

func TestDefaultValueExpressionCuidAndDBGenerated(t *testing.T) {
	expr := "gen_random_uuid()::text"
	for _, d := range []interface {
		DefaultValueExpression(string, []string) string
		Name() string
	}{PostgreSQL{}, MySQL{}, SQLite{}} {
		if got := d.DefaultValueExpression("cuid", nil); got != "" {
			t.Errorf("%s cuid default = %q, want empty (client-generated)", d.Name(), got)
		}
		if got := d.DefaultValueExpression("dbgenerated", []string{expr}); got != expr {
			t.Errorf("%s dbgenerated = %q, want %q", d.Name(), got, expr)
		}
		if got := d.DefaultValueExpression("dbgenerated", []string{`"coalesce(a, 'x')"`}); got != "coalesce(a, 'x')" {
			t.Errorf("%s quoted dbgenerated = %q", d.Name(), got)
		}
		if got := d.DefaultValueExpression("dbgenerated", []string{`"a" || "b"`}); got != `"a" || "b"` {
			t.Errorf("%s identifier dbgenerated = %q", d.Name(), got)
		}
		if got := d.DefaultValueExpression("dbgenerated", []string{"coalesce(a", "'x')"}); got != "coalesce(a, 'x')" {
			t.Errorf("%s split dbgenerated = %q", d.Name(), got)
		}
	}
}

func TestPostgreSQLSequenceDefaultEscapesName(t *testing.T) {
	pg := PostgreSQL{}
	if got := pg.DefaultValueExpression("sequence", []string{"o'brien_seq"}); got != "nextval('o''brien_seq')" {
		t.Fatalf("sequence = %q", got)
	}
	if got := pg.DefaultValueExpression("sequence", []string{`"audit.my_seq"`}); got != "nextval('audit.my_seq')" {
		t.Fatalf("quoted sequence = %q", got)
	}
}
