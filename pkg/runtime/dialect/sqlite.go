package dialect

import (
	"database/sql"
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/arsfy/gcorm/pkg/runtime"
	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
)

// SQLite implements the runtime.Dialect interface for SQLite databases.
type SQLite struct{}

func (SQLite) Name() string { return "sqlite" }

func (SQLite) Placeholder(_ int) string { return "?" }

func (SQLite) QuoteIdent(parts ...string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = `"` + p + `"`
	}
	return strings.Join(quoted, ".")
}

func (SQLite) SupportsReturning() bool { return false }

func (SQLite) SupportsSchemas() bool { return false }

func (SQLite) RewriteLimitOffset(limit, offset *int) string {
	var sb strings.Builder
	if limit != nil {
		sb.WriteString(fmt.Sprintf("LIMIT %d", *limit))
	}
	if offset != nil {
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		// SQLite requires LIMIT before OFFSET; use -1 for unbounded.
		if limit == nil {
			sb.WriteString("LIMIT -1 ")
		}
		sb.WriteString(fmt.Sprintf("OFFSET %d", *offset))
	}
	return sb.String()
}

func (SQLite) ClassifyError(err error) *runtime.DBError {
	if err == nil {
		return nil
	}

	if stderrors.Is(err, sql.ErrNoRows) {
		return gcoerr.Wrap(err, gcoerr.CodeNotFound, "record not found")
	}

	msg := err.Error()

	switch {
	case containsAny(msg, "unique constraint failed", "2067"):
		e := gcoerr.Wrap(err, gcoerr.CodeUniqueViolation, "unique constraint violated")
		// SQLite format: "UNIQUE constraint failed: table.column"
		if idx := strings.Index(msg, "UNIQUE constraint failed:"); idx >= 0 {
			detail := strings.TrimSpace(msg[idx+len("UNIQUE constraint failed:"):])
			if detail != "" {
				e = e.WithMeta("constraint", detail)
			}
		}
		return e

	case containsAny(msg, "foreign key constraint failed", "787"):
		return gcoerr.Wrap(err, gcoerr.CodeForeignKeyViolation, "foreign key constraint violated")

	case containsAny(msg, "not null constraint failed", "1299"):
		return gcoerr.Wrap(err, gcoerr.CodeNotNullViolation, "not-null constraint violated")

	case containsAny(msg, "check constraint failed", "275"):
		return gcoerr.Wrap(err, gcoerr.CodeCheckViolation, "check constraint violated")

	case containsAny(msg, "database is locked", "database table is locked", "(5)", "(6)"):
		return gcoerr.Wrap(err, gcoerr.CodeDeadlock, "database locked")

	case containsAny(msg, "disk i/o error", "unable to open database", "(10)"):
		return gcoerr.Wrap(err, gcoerr.CodeConnectionError, "connection error")
	}

	return nil
}

func (SQLite) TypeMapping(schemaType string, isOptional bool) string {
	var t string
	switch schemaType {
	case "String":
		t = "TEXT"
	case "Int":
		t = "INTEGER"
	case "BigInt":
		t = "INTEGER"
	case "Float":
		t = "REAL"
	case "Decimal":
		t = "TEXT"
	case "Boolean":
		t = "INTEGER"
	case "DateTime":
		t = "TEXT"
	case "Bytes":
		t = "BLOB"
	case "Json":
		t = "TEXT"
	case "UUID":
		t = "TEXT"
	default:
		t = "TEXT"
	}
	return t
}

func (SQLite) DefaultValueExpression(funcName string, args []string) string {
	switch funcName {
	case "uuid":
		return "(lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab',abs(random()) % 4 + 1,1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6))))"
	case "now":
		return "(datetime('now'))"
	case "autoincrement":
		return "AUTOINCREMENT"
	default:
		return funcName + "()"
	}
}

func (SQLite) SupportsFeature(feature runtime.Feature) bool {
	switch feature {
	case runtime.FeatureCascadeDelete,
		runtime.FeatureUpsert:
		return true
	case runtime.FeatureReturning,
		runtime.FeatureSchemas,
		runtime.FeatureJSON,
		runtime.FeatureUUID,
		runtime.FeatureArrays,
		runtime.FeatureEnumType,
		runtime.FeatureSerialType,
		runtime.FeatureIdentityColumn,
		runtime.FeaturePartialIndex:
		return false
	default:
		return false
	}
}

// Compile-time check.
var _ runtime.Dialect = SQLite{}
