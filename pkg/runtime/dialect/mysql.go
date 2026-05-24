package dialect

import (
	"database/sql"
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/arsfy/gcorm/pkg/runtime"
	gcoerr "github.com/arsfy/gcorm/pkg/runtime/errors"
)

// MySQL implements the runtime.Dialect interface for MySQL databases.
type MySQL struct{}

func (MySQL) Name() string { return "mysql" }

func (MySQL) Placeholder(_ int) string { return "?" }

func (MySQL) QuoteIdent(parts ...string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = "`" + p + "`"
	}
	return strings.Join(quoted, ".")
}

func (MySQL) SupportsReturning() bool { return false }

func (MySQL) SupportsSchemas() bool { return true }

func (MySQL) RewriteLimitOffset(limit, offset *int) string {
	var sb strings.Builder
	if limit != nil {
		sb.WriteString(fmt.Sprintf("LIMIT %d", *limit))
	}
	if offset != nil {
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(fmt.Sprintf("OFFSET %d", *offset))
	}
	return sb.String()
}

func (MySQL) ClassifyError(err error) *runtime.DBError {
	if err == nil {
		return nil
	}

	if stderrors.Is(err, sql.ErrNoRows) {
		return gcoerr.Wrap(err, gcoerr.CodeNotFound, "record not found")
	}

	msg := err.Error()

	switch {
	case containsAny(msg, "1062", "duplicate entry"):
		e := gcoerr.Wrap(err, gcoerr.CodeUniqueViolation, "unique constraint violated")
		if k := extractBetweenQuotes(msg, "for key"); k != "" {
			e = e.WithMeta("constraint", k)
		}
		return e

	case containsAny(msg, "1451", "1452"):
		e := gcoerr.Wrap(err, gcoerr.CodeForeignKeyViolation, "foreign key constraint violated")
		if c := extractBetweenQuotes(msg, "constraint"); c != "" {
			e = e.WithMeta("constraint", c)
		}
		return e

	case containsAny(msg, "1048", "column cannot be null"):
		return gcoerr.Wrap(err, gcoerr.CodeNotNullViolation, "not-null constraint violated")

	case containsAny(msg, "1644", "check constraint"):
		return gcoerr.Wrap(err, gcoerr.CodeCheckViolation, "check constraint violated")

	case containsAny(msg, "1213", "deadlock"):
		return gcoerr.Wrap(err, gcoerr.CodeDeadlock, "deadlock detected")

	case containsAny(msg, "1205", "lock wait timeout"):
		return gcoerr.Wrap(err, gcoerr.CodeTimeout, "lock wait timeout")

	case containsAny(msg, "1406", "data too long"):
		return gcoerr.Wrap(err, gcoerr.CodeDataTruncation, "data truncation")

	case containsAny(msg, "2002", "2003", "2006"):
		return gcoerr.Wrap(err, gcoerr.CodeConnectionError, "connection error")
	}

	return nil
}

func (MySQL) TypeMapping(schemaType string, isOptional bool) string {
	var t string
	switch schemaType {
	case "String":
		t = "VARCHAR(191)"
	case "Int":
		t = "INTEGER"
	case "SmallInt":
		t = "SMALLINT"
	case "BigInt":
		t = "BIGINT"
	case "Float":
		t = "DOUBLE"
	case "Decimal":
		t = "DECIMAL"
	case "Boolean":
		t = "BOOLEAN"
	case "DateTime":
		t = "DATETIME(3)"
	case "Bytes":
		t = "LONGBLOB"
	case "Json":
		t = "JSON"
	case "UUID":
		t = "VARCHAR(36)"
	default:
		t = "VARCHAR(191)"
	}
	return t
}

func (MySQL) DefaultValueExpression(funcName string, args []string) string {
	switch funcName {
	case "uuid":
		return "(UUID())"
	case "now":
		return "NOW(3)"
	case "autoincrement":
		return "AUTO_INCREMENT"
	default:
		return funcName + "()"
	}
}

func (MySQL) SupportsFeature(feature runtime.Feature) bool {
	switch feature {
	case runtime.FeatureJSON,
		runtime.FeatureCascadeDelete,
		runtime.FeatureSerialType,
		runtime.FeatureUpsert,
		runtime.FeatureSchemas:
		return true
	case runtime.FeatureReturning,
		runtime.FeatureUUID,
		runtime.FeatureArrays,
		runtime.FeatureEnumType,
		runtime.FeatureIdentityColumn,
		runtime.FeaturePartialIndex:
		return false
	default:
		return false
	}
}

// Compile-time check.
var _ runtime.Dialect = MySQL{}
