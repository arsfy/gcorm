package errors

import (
	"errors"
	"fmt"
)

// ErrorCode represents a classified database error category.
type ErrorCode int

const (
	CodeUnknown ErrorCode = iota
	CodeUniqueViolation
	CodeForeignKeyViolation
	CodeNotFound
	CodeSerializationFailure
	CodeDeadlock
	CodeConnectionError
	CodeTimeout
	CodeMigrationDrift
	CodeInvalidSchema
	CodeUnsupportedFeature
	CodeCheckViolation
	CodeNotNullViolation
	CodeDataTruncation
)

var codeNames = map[ErrorCode]string{
	CodeUnknown:              "Unknown",
	CodeUniqueViolation:      "UniqueViolation",
	CodeForeignKeyViolation:  "ForeignKeyViolation",
	CodeNotFound:             "NotFound",
	CodeSerializationFailure: "SerializationFailure",
	CodeDeadlock:             "Deadlock",
	CodeConnectionError:      "ConnectionError",
	CodeTimeout:              "Timeout",
	CodeMigrationDrift:       "MigrationDrift",
	CodeInvalidSchema:        "InvalidSchema",
	CodeUnsupportedFeature:   "UnsupportedFeature",
	CodeCheckViolation:       "CheckViolation",
	CodeNotNullViolation:     "NotNullViolation",
	CodeDataTruncation:       "DataTruncation",
}

// String returns the human-readable name of the error code.
func (c ErrorCode) String() string {
	if name, ok := codeNames[c]; ok {
		return name
	}
	return fmt.Sprintf("ErrorCode(%d)", int(c))
}

// DBError is the unified ORM error type that wraps raw database errors
// with classification, context, and user-friendly messaging.
type DBError struct {
	// Code is the classified error category.
	Code ErrorCode

	// Message is a user-friendly error message.
	Message string

	// Detail provides additional diagnostic context.
	Detail string

	// Cause is the original error from the database driver.
	Cause error

	// Meta holds additional metadata (e.g., constraint name, table, column).
	Meta map[string]string
}

// Error returns a formatted error string including the code, message,
// detail (if present), and cause (if present).
func (e *DBError) Error() string {
	s := fmt.Sprintf("gco: %s: %s", e.Code, e.Message)
	if e.Detail != "" {
		s += " (" + e.Detail + ")"
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap returns the underlying cause, supporting errors.Unwrap.
func (e *DBError) Unwrap() error { return e.Cause }

// Is checks if the error matches a specific error code.
// It walks the error chain using errors.As to find a *DBError
// and compares its Code field.
func Is(err error, code ErrorCode) bool {
	var dbErr *DBError
	if errors.As(err, &dbErr) {
		return dbErr.Code == code
	}
	return false
}

// AsDBError attempts to extract a *DBError from an error chain.
func AsDBError(err error) (*DBError, bool) {
	var dbErr *DBError
	if errors.As(err, &dbErr) {
		return dbErr, true
	}
	return nil, false
}

// New creates a new DBError with the given code and message.
func New(code ErrorCode, message string) *DBError {
	return &DBError{
		Code:    code,
		Message: message,
	}
}

// Wrap wraps a raw error with classification.
func Wrap(cause error, code ErrorCode, message string) *DBError {
	return &DBError{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// WithMeta returns a copy of the error with additional metadata.
func (e *DBError) WithMeta(key, value string) *DBError {
	cp := *e
	cp.Meta = make(map[string]string, len(e.Meta)+1)
	for k, v := range e.Meta {
		cp.Meta[k] = v
	}
	cp.Meta[key] = value
	return &cp
}

// WithDetail returns a copy of the error with a detail message.
func (e *DBError) WithDetail(detail string) *DBError {
	cp := *e
	cp.Detail = detail
	return &cp
}

// ErrNotFound is a convenience sentinel for not-found errors.
var ErrNotFound = New(CodeNotFound, "record not found")

// ErrUniqueViolation is a convenience sentinel for unique constraint violations.
var ErrUniqueViolation = New(CodeUniqueViolation, "unique constraint violated")

// IsNotFound reports whether err is a DBError with CodeNotFound.
func IsNotFound(err error) bool { return Is(err, CodeNotFound) }

// IsUniqueViolation reports whether err is a DBError with CodeUniqueViolation.
func IsUniqueViolation(err error) bool { return Is(err, CodeUniqueViolation) }

// IsForeignKeyViolation reports whether err is a DBError with CodeForeignKeyViolation.
func IsForeignKeyViolation(err error) bool { return Is(err, CodeForeignKeyViolation) }

// IsConnectionError reports whether err is a DBError with CodeConnectionError.
func IsConnectionError(err error) bool { return Is(err, CodeConnectionError) }

// IsTimeout reports whether err is a DBError with CodeTimeout.
func IsTimeout(err error) bool { return Is(err, CodeTimeout) }
