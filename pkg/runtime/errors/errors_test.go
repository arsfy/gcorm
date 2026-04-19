package errors

import (
	"errors"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// 1. ErrorCode.String() for all codes
// ---------------------------------------------------------------------------

func TestErrorCode_String(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want string
	}{
		{CodeUnknown, "Unknown"},
		{CodeUniqueViolation, "UniqueViolation"},
		{CodeForeignKeyViolation, "ForeignKeyViolation"},
		{CodeNotFound, "NotFound"},
		{CodeSerializationFailure, "SerializationFailure"},
		{CodeDeadlock, "Deadlock"},
		{CodeConnectionError, "ConnectionError"},
		{CodeTimeout, "Timeout"},
		{CodeMigrationDrift, "MigrationDrift"},
		{CodeInvalidSchema, "InvalidSchema"},
		{CodeUnsupportedFeature, "UnsupportedFeature"},
		{CodeCheckViolation, "CheckViolation"},
		{CodeNotNullViolation, "NotNullViolation"},
		{CodeDataTruncation, "DataTruncation"},
	}
	for _, tc := range cases {
		if got := tc.code.String(); got != tc.want {
			t.Errorf("ErrorCode(%d).String() = %q, want %q", int(tc.code), got, tc.want)
		}
	}

	// Unknown numeric code should fall back to the formatted representation.
	unknown := ErrorCode(999)
	want := "ErrorCode(999)"
	if got := unknown.String(); got != want {
		t.Errorf("ErrorCode(999).String() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// 2. DBError.Error() formatting
// ---------------------------------------------------------------------------

func TestDBError_Error(t *testing.T) {
	// Basic: code + message only.
	e := &DBError{Code: CodeNotFound, Message: "record not found"}
	want := "gco: NotFound: record not found"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	// With detail.
	e2 := &DBError{Code: CodeUniqueViolation, Message: "duplicate key", Detail: "constraint idx_users_email"}
	want2 := "gco: UniqueViolation: duplicate key (constraint idx_users_email)"
	if got := e2.Error(); got != want2 {
		t.Errorf("Error() = %q, want %q", got, want2)
	}

	// With cause.
	cause := fmt.Errorf("pq: connection refused")
	e3 := &DBError{Code: CodeConnectionError, Message: "cannot connect", Cause: cause}
	want3 := "gco: ConnectionError: cannot connect: pq: connection refused"
	if got := e3.Error(); got != want3 {
		t.Errorf("Error() = %q, want %q", got, want3)
	}

	// With both detail and cause.
	e4 := &DBError{Code: CodeTimeout, Message: "query timed out", Detail: "after 30s", Cause: fmt.Errorf("context deadline exceeded")}
	want4 := "gco: Timeout: query timed out (after 30s): context deadline exceeded"
	if got := e4.Error(); got != want4 {
		t.Errorf("Error() = %q, want %q", got, want4)
	}
}

// ---------------------------------------------------------------------------
// 3. DBError.Unwrap() returns cause
// ---------------------------------------------------------------------------

func TestDBError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("raw driver error")
	e := Wrap(cause, CodeUnknown, "something failed")
	if e.Unwrap() != cause {
		t.Error("Unwrap() did not return the original cause")
	}

	// nil cause
	e2 := New(CodeNotFound, "no rows")
	if e2.Unwrap() != nil {
		t.Error("Unwrap() should return nil when there is no cause")
	}
}

// ---------------------------------------------------------------------------
// 4. Is() with matching and non-matching codes
// ---------------------------------------------------------------------------

func TestIs(t *testing.T) {
	e := New(CodeDeadlock, "deadlock detected")
	if !Is(e, CodeDeadlock) {
		t.Error("Is() should return true for matching code")
	}
	if Is(e, CodeTimeout) {
		t.Error("Is() should return false for non-matching code")
	}

	// Non-DBError
	plain := fmt.Errorf("plain error")
	if Is(plain, CodeUnknown) {
		t.Error("Is() should return false for non-DBError")
	}
}

// ---------------------------------------------------------------------------
// 5. AsDBError() with DBError, wrapped DBError, and non-DBError
// ---------------------------------------------------------------------------

func TestAsDBError(t *testing.T) {
	// Direct DBError.
	e := New(CodeNotFound, "not found")
	got, ok := AsDBError(e)
	if !ok || got != e {
		t.Error("AsDBError should extract direct *DBError")
	}

	// Wrapped with fmt.Errorf %w.
	wrapped := fmt.Errorf("outer: %w", e)
	got2, ok2 := AsDBError(wrapped)
	if !ok2 {
		t.Fatal("AsDBError should find *DBError through fmt.Errorf wrapping")
	}
	if got2 != e {
		t.Error("AsDBError should return the original *DBError from the chain")
	}

	// Non-DBError.
	plain := fmt.Errorf("not a db error")
	_, ok3 := AsDBError(plain)
	if ok3 {
		t.Error("AsDBError should return false for non-DBError")
	}
}

// ---------------------------------------------------------------------------
// 6. New() creates correct error
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	e := New(CodeInvalidSchema, "bad schema")
	if e.Code != CodeInvalidSchema {
		t.Errorf("Code = %v, want %v", e.Code, CodeInvalidSchema)
	}
	if e.Message != "bad schema" {
		t.Errorf("Message = %q, want %q", e.Message, "bad schema")
	}
	if e.Cause != nil {
		t.Error("Cause should be nil")
	}
	if e.Detail != "" {
		t.Error("Detail should be empty")
	}
	if e.Meta != nil {
		t.Error("Meta should be nil")
	}
}

// ---------------------------------------------------------------------------
// 7. Wrap() preserves cause and sets code
// ---------------------------------------------------------------------------

func TestWrap(t *testing.T) {
	cause := fmt.Errorf("driver: unique_violation")
	e := Wrap(cause, CodeUniqueViolation, "duplicate entry")
	if e.Code != CodeUniqueViolation {
		t.Errorf("Code = %v, want %v", e.Code, CodeUniqueViolation)
	}
	if e.Message != "duplicate entry" {
		t.Errorf("Message = %q, want %q", e.Message, "duplicate entry")
	}
	if e.Cause != cause {
		t.Error("Cause should be the original error")
	}
}

// ---------------------------------------------------------------------------
// 8. WithMeta() adds metadata
// ---------------------------------------------------------------------------

func TestWithMeta(t *testing.T) {
	e := New(CodeUniqueViolation, "dup")
	e2 := e.WithMeta("constraint", "idx_email")
	e3 := e2.WithMeta("table", "users")

	// Original should not be mutated.
	if e.Meta != nil {
		t.Error("original Meta should remain nil")
	}

	// e2 should have one entry.
	if v, ok := e2.Meta["constraint"]; !ok || v != "idx_email" {
		t.Errorf("e2.Meta[constraint] = %q, want %q", v, "idx_email")
	}
	if len(e2.Meta) != 1 {
		t.Errorf("e2 should have 1 meta entry, got %d", len(e2.Meta))
	}

	// e3 should have both entries.
	if len(e3.Meta) != 2 {
		t.Errorf("e3 should have 2 meta entries, got %d", len(e3.Meta))
	}
	if v := e3.Meta["table"]; v != "users" {
		t.Errorf("e3.Meta[table] = %q, want %q", v, "users")
	}
}

// ---------------------------------------------------------------------------
// 9. WithDetail() adds detail
// ---------------------------------------------------------------------------

func TestWithDetail(t *testing.T) {
	e := New(CodeTimeout, "timed out")
	e2 := e.WithDetail("after 5s")

	if e.Detail != "" {
		t.Error("original Detail should remain empty")
	}
	if e2.Detail != "after 5s" {
		t.Errorf("Detail = %q, want %q", e2.Detail, "after 5s")
	}
	// Code and message should be preserved.
	if e2.Code != CodeTimeout || e2.Message != "timed out" {
		t.Error("Code or Message was not preserved")
	}
}

// ---------------------------------------------------------------------------
// 10. Predicate helpers
// ---------------------------------------------------------------------------

func TestPredicates(t *testing.T) {
	tests := []struct {
		name string
		err  error
		fn   func(error) bool
		want bool
	}{
		{"IsNotFound true", New(CodeNotFound, "x"), IsNotFound, true},
		{"IsNotFound false", New(CodeTimeout, "x"), IsNotFound, false},
		{"IsUniqueViolation true", New(CodeUniqueViolation, "x"), IsUniqueViolation, true},
		{"IsUniqueViolation false", New(CodeNotFound, "x"), IsUniqueViolation, false},
		{"IsForeignKeyViolation true", New(CodeForeignKeyViolation, "x"), IsForeignKeyViolation, true},
		{"IsForeignKeyViolation false", New(CodeNotFound, "x"), IsForeignKeyViolation, false},
		{"IsConnectionError true", New(CodeConnectionError, "x"), IsConnectionError, true},
		{"IsConnectionError false", New(CodeNotFound, "x"), IsConnectionError, false},
		{"IsTimeout true", New(CodeTimeout, "x"), IsTimeout, true},
		{"IsTimeout false", New(CodeNotFound, "x"), IsTimeout, false},
		// Through wrapping.
		{"IsNotFound wrapped", fmt.Errorf("wrap: %w", New(CodeNotFound, "x")), IsNotFound, true},
		// Plain error.
		{"plain error", fmt.Errorf("plain"), IsNotFound, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(tc.err); got != tc.want {
				t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 11. Standard library errors.Is / errors.As compatibility
// ---------------------------------------------------------------------------

func TestStdLibErrorsAs(t *testing.T) {
	original := New(CodeDeadlock, "deadlock")
	wrapped := fmt.Errorf("transaction failed: %w", original)

	var target *DBError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As should find *DBError in chain")
	}
	if target.Code != CodeDeadlock {
		t.Errorf("Code = %v, want %v", target.Code, CodeDeadlock)
	}
}

func TestStdLibErrorsIs(t *testing.T) {
	cause := fmt.Errorf("io timeout")
	e := Wrap(cause, CodeTimeout, "query timed out")

	// errors.Is should walk the chain and find the original cause.
	if !errors.Is(e, cause) {
		t.Error("errors.Is should find the wrapped cause")
	}
}

// ---------------------------------------------------------------------------
// 12. Error chain: Wrap(Wrap(original, ...), ...) — AsDBError finds outermost
// ---------------------------------------------------------------------------

func TestErrorChainNestedWrap(t *testing.T) {
	original := fmt.Errorf("raw driver error")
	inner := Wrap(original, CodeConnectionError, "connection lost")
	outer := Wrap(inner, CodeSerializationFailure, "transaction aborted")

	// AsDBError should find the outermost *DBError first.
	got, ok := AsDBError(outer)
	if !ok {
		t.Fatal("AsDBError should find a *DBError in the chain")
	}
	if got != outer {
		t.Error("AsDBError should return the outermost *DBError")
	}
	if got.Code != CodeSerializationFailure {
		t.Errorf("Code = %v, want %v", got.Code, CodeSerializationFailure)
	}

	// The inner DBError should be reachable via Unwrap.
	innerGot, ok := AsDBError(got.Unwrap())
	if !ok {
		t.Fatal("AsDBError should find the inner *DBError")
	}
	if innerGot.Code != CodeConnectionError {
		t.Errorf("inner Code = %v, want %v", innerGot.Code, CodeConnectionError)
	}

	// The original cause should be at the bottom.
	if !errors.Is(outer, original) {
		t.Error("errors.Is should find the original cause at the bottom of the chain")
	}
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

func TestSentinelErrors(t *testing.T) {
	if ErrNotFound.Code != CodeNotFound {
		t.Error("ErrNotFound should have CodeNotFound")
	}
	if ErrUniqueViolation.Code != CodeUniqueViolation {
		t.Error("ErrUniqueViolation should have CodeUniqueViolation")
	}
}
