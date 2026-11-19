package dialect

import (
	stderrors "errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/arsfy/gcorm/pkg/runtime"
	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
)

// postgresErrorDetails holds the diagnostics a PostgreSQL error exposes.
type postgresErrorDetails struct {
	SQLState   string
	Constraint string
	Table      string
	Column     string
}

// postgresSQLStateInMessage matches the "(SQLSTATE 23505)" suffix pgx adds to
// PgError.Error() and similar code annotations in wrapped error text.
var postgresSQLStateInMessage = regexp.MustCompile(`\bSQLSTATE\s*[:=]?\s*([0-9A-Za-z]{5})\b`)

// postgresErrorDetailsFromDriver extracts the SQLSTATE code and constraint
// metadata. It tries, in order: any error in the chain implementing
// SQLState() (pgx *pgconn.PgError), a struct in the chain with a string Code
// field holding a SQLSTATE (lib/pq *pq.Error), and finally a SQLSTATE
// annotation in the error text outside quoted identifiers.
func postgresErrorDetailsFromDriver(err error) (postgresErrorDetails, bool) {
	var stater interface{ SQLState() string }
	if stderrors.As(err, &stater) {
		if code := strings.ToUpper(stater.SQLState()); isSQLState(code) {
			details := postgresErrorDetailsFromStruct(stater)
			details.SQLState = code
			return details, true
		}
	}

	var found postgresErrorDetails
	ok := walkErrorChain(err, func(e error) bool {
		code := strings.ToUpper(structStringField(e, "Code"))
		if !isSQLState(code) {
			return false
		}
		found = postgresErrorDetailsFromStruct(e)
		found.SQLState = code
		return true
	})
	if ok {
		return found, true
	}

	msg := err.Error()
	masked := maskDoubleQuoted(msg)
	if m := postgresSQLStateInMessage.FindStringSubmatch(masked); m != nil {
		if code := strings.ToUpper(m[1]); isSQLState(code) {
			details := postgresErrorDetails{SQLState: code}
			lowerMasked := asciiLower(masked)
			for _, marker := range []string{"unique constraint", "foreign key constraint", "check constraint", "exclusion constraint"} {
				if name := quotedNameAfter(msg, lowerMasked, marker); name != "" {
					details.Constraint = name
					break
				}
			}
			return details, true
		}
	}
	return postgresErrorDetails{}, false
}

func postgresErrorDetailsFromStruct(v any) postgresErrorDetails {
	return postgresErrorDetails{
		Constraint: structStringField(v, "ConstraintName", "Constraint"),
		Table:      structStringField(v, "TableName", "Table"),
		Column:     structStringField(v, "ColumnName", "Column"),
	}
}

// classifyPostgresSQLState maps a SQLSTATE code to a DBError. Unmapped codes
// return nil so callers can fall back to their own handling.
func classifyPostgresSQLState(err error, details postgresErrorDetails) *runtime.DBError {
	var e *runtime.DBError
	switch code := details.SQLState; {
	case code == "23505":
		e = gcoerr.Wrap(err, gcoerr.CodeUniqueViolation, "unique constraint violated")
	case code == "23503":
		e = gcoerr.Wrap(err, gcoerr.CodeForeignKeyViolation, "foreign key constraint violated")
	case code == "23502":
		e = gcoerr.Wrap(err, gcoerr.CodeNotNullViolation, "not-null constraint violated")
	case code == "23514":
		e = gcoerr.Wrap(err, gcoerr.CodeCheckViolation, "check constraint violated")
	case code == "23P01":
		e = gcoerr.Wrap(err, gcoerr.CodeExclusionViolation, "exclusion constraint violated")
	case code == "22001":
		e = gcoerr.Wrap(err, gcoerr.CodeDataTruncation, "data truncation")
	case code == "22003":
		e = gcoerr.Wrap(err, gcoerr.CodeNumericOutOfRange, "numeric value out of range")
	case code == "22P02":
		e = gcoerr.Wrap(err, gcoerr.CodeInvalidTextRepresentation, "invalid input value")
	case code == "40001":
		e = gcoerr.Wrap(err, gcoerr.CodeSerializationFailure, "serialization failure")
	case code == "40P01":
		e = gcoerr.Wrap(err, gcoerr.CodeDeadlock, "deadlock detected")
	case code == "57014":
		e = gcoerr.Wrap(err, gcoerr.CodeTimeout, "query canceled")
	case code == "53300", strings.HasPrefix(code, "08"):
		e = gcoerr.Wrap(err, gcoerr.CodeConnectionError, "connection error")
	default:
		return nil
	}
	e = e.WithMeta("sqlstate", details.SQLState)
	if details.Constraint != "" {
		e = e.WithMeta("constraint", details.Constraint)
	}
	if details.Table != "" {
		e = e.WithMeta("table", details.Table)
	}
	if details.Column != "" {
		e = e.WithMeta("column", details.Column)
	}
	return e
}

// postgresMessageRule classifies a code-less PostgreSQL error by the
// server's English message text. Phrases are matched only outside
// double-quoted identifiers, so a constraint named "unique_violation_chk"
// cannot change the classification.
type postgresMessageRule struct {
	phrases          []string
	code             gcoerr.ErrorCode
	message          string
	constraintMarker string
}

var postgresMessageRules = []postgresMessageRule{
	{phrases: []string{"violates unique constraint", "duplicate key value"}, code: gcoerr.CodeUniqueViolation, message: "unique constraint violated", constraintMarker: "unique constraint"},
	{phrases: []string{"violates foreign key constraint"}, code: gcoerr.CodeForeignKeyViolation, message: "foreign key constraint violated", constraintMarker: "foreign key constraint"},
	{phrases: []string{"violates not-null constraint", "null value in column"}, code: gcoerr.CodeNotNullViolation, message: "not-null constraint violated"},
	{phrases: []string{"violates check constraint"}, code: gcoerr.CodeCheckViolation, message: "check constraint violated", constraintMarker: "check constraint"},
	{phrases: []string{"violates exclusion constraint"}, code: gcoerr.CodeExclusionViolation, message: "exclusion constraint violated", constraintMarker: "exclusion constraint"},
	{phrases: []string{"value too long for type"}, code: gcoerr.CodeDataTruncation, message: "data truncation"},
	{phrases: []string{"out of range for type", "integer out of range", "smallint out of range", "bigint out of range", "value out of range", "numeric field overflow"}, code: gcoerr.CodeNumericOutOfRange, message: "numeric value out of range"},
	{phrases: []string{"invalid input syntax for", "invalid input value for"}, code: gcoerr.CodeInvalidTextRepresentation, message: "invalid input value"},
	{phrases: []string{"could not serialize access"}, code: gcoerr.CodeSerializationFailure, message: "serialization failure"},
	{phrases: []string{"deadlock detected"}, code: gcoerr.CodeDeadlock, message: "deadlock detected"},
	{phrases: []string{"canceling statement due to"}, code: gcoerr.CodeTimeout, message: "query canceled"},
	{phrases: []string{"too many clients already", "remaining connection slots are reserved", "connection refused", "connection reset", "broken pipe"}, code: gcoerr.CodeConnectionError, message: "connection error"},
}

func classifyPostgresMessage(err error) *runtime.DBError {
	msg := err.Error()
	masked := asciiLower(maskDoubleQuoted(msg))
	for _, rule := range postgresMessageRules {
		if !containsAnyPhrase(masked, rule.phrases) {
			continue
		}
		e := gcoerr.Wrap(err, rule.code, rule.message)
		if rule.constraintMarker != "" {
			if name := quotedNameAfter(msg, masked, rule.constraintMarker); name != "" {
				e = e.WithMeta("constraint", name)
			}
		}
		return e
	}
	return nil
}

func containsAnyPhrase(s string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

// quotedNameAfter returns the double-quoted name following marker in msg.
// masked must be the byte-aligned, quote-masked, lower-cased form of msg so
// the marker is never found inside an identifier.
func quotedNameAfter(msg, masked, marker string) string {
	idx := strings.Index(masked, marker)
	if idx < 0 {
		return ""
	}
	pos := idx + len(marker)
	for pos < len(msg) && msg[pos] == ' ' {
		pos++
	}
	if pos >= len(msg) || msg[pos] != '"' {
		return ""
	}
	end := strings.IndexByte(msg[pos+1:], '"')
	if end < 0 {
		return ""
	}
	return msg[pos+1 : pos+1+end]
}

// maskDoubleQuoted replaces the bytes inside double-quoted segments with 'x'
// while keeping byte offsets aligned with the input.
func maskDoubleQuoted(s string) string {
	b := []byte(s)
	inQuote := false
	for i, ch := range b {
		switch {
		case ch == '"':
			inQuote = !inQuote
		case inQuote:
			b[i] = 'x'
		}
	}
	return string(b)
}

// asciiLower lower-cases ASCII letters only, preserving byte offsets.
func asciiLower(s string) string {
	b := []byte(s)
	for i, ch := range b {
		if ch >= 'A' && ch <= 'Z' {
			b[i] = ch + ('a' - 'A')
		}
	}
	return string(b)
}

func isSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for i := 0; i < len(code); i++ {
		ch := code[i]
		if (ch < '0' || ch > '9') && (ch < 'A' || ch > 'Z') {
			return false
		}
	}
	return true
}

// walkErrorChain visits err and every error reachable through Unwrap() error
// and Unwrap() []error until visit returns true.
func walkErrorChain(err error, visit func(error) bool) bool {
	for err != nil {
		if visit(err) {
			return true
		}
		switch u := err.(type) {
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				if walkErrorChain(inner, visit) {
					return true
				}
			}
			return false
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		default:
			return false
		}
	}
	return false
}

// structStringField returns the first exported string-kinded field of v
// (dereferencing pointers/interfaces) among names, or "".
func structStringField(v any, names ...string) string {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	for _, name := range names {
		sf, ok := rv.Type().FieldByName(name)
		if !ok || !sf.IsExported() {
			continue
		}
		field, err := rv.FieldByIndexErr(sf.Index)
		if err == nil && field.Kind() == reflect.String {
			return field.String()
		}
	}
	return ""
}
