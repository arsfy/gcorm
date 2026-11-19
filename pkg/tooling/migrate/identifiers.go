package migrate

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// MaxIdentifierBytes is PostgreSQL's identifier limit (NAMEDATALEN - 1).
// MySQL allows 64 characters, so names within this limit fit every dialect.
const MaxIdentifierBytes = 63

// LimitIdentifier shortens a generated object name that exceeds
// MaxIdentifierBytes. Longer names keep a prefix, cut at a UTF-8 boundary,
// followed by "_" and the first 8 hex digits of the SHA-1 of the full name, so
// distinct long names stay distinct. Names within the limit are unchanged.
func LimitIdentifier(name string) string {
	if len(name) <= MaxIdentifierBytes {
		return name
	}
	sum := sha1.Sum([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	return clipUTF8(name, MaxIdentifierBytes-len(suffix)) + suffix
}

// clipUTF8 returns the longest prefix of s within limit bytes that does not
// split a multi-byte UTF-8 sequence.
func clipUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// DefaultIndexName is the generated name of an unnamed @@index (or of an
// unnamed @@unique index when unique is true).
func DefaultIndexName(table string, columns []string, unique bool) string {
	prefix := "idx_"
	if unique {
		prefix = "uq_"
	}
	return LimitIdentifier(prefix + table + "_" + strings.Join(columns, "_"))
}

// DefaultUniqueName is the generated name of an unnamed unique constraint.
func DefaultUniqueName(table string, columns []string) string {
	return LimitIdentifier("uq_" + table + "_" + strings.Join(columns, "_"))
}

// DefaultForeignKeyName is the generated name of an unnamed foreign key.
func DefaultForeignKeyName(table string, columns []string) string {
	return LimitIdentifier("fk_" + table + "_" + strings.Join(columns, "_"))
}

// shortensGeneratedNames reports whether generated names are shortened with
// LimitIdentifier for provider. Only PostgreSQL truncates long identifiers
// (silently); MySQL and SQLite keep the full names older versions created,
// so existing databases keep matching (MySQL rejects over-long names
// outright, as before). An unknown provider follows PostgreSQL.
func shortensGeneratedNames(provider string) bool {
	return provider != "mysql" && provider != "sqlite"
}

// DefaultIndexNameFor is DefaultIndexName for a provider: MySQL and SQLite
// keep the full, unshortened name.
func DefaultIndexNameFor(provider, table string, columns []string, unique bool) string {
	if shortensGeneratedNames(provider) {
		return DefaultIndexName(table, columns, unique)
	}
	prefix := "idx_"
	if unique {
		prefix = "uq_"
	}
	return prefix + table + "_" + strings.Join(columns, "_")
}

// DefaultUniqueNameFor is DefaultUniqueName for a provider.
func DefaultUniqueNameFor(provider, table string, columns []string) string {
	if shortensGeneratedNames(provider) {
		return DefaultUniqueName(table, columns)
	}
	return "uq_" + table + "_" + strings.Join(columns, "_")
}

// DefaultForeignKeyNameFor is DefaultForeignKeyName for a provider. MySQL
// keeps the comma-joined name older versions created so existing foreign
// keys can still be dropped by name (MySQL has no DROP FOREIGN KEY IF EXISTS).
func DefaultForeignKeyNameFor(provider, table string, columns []string) string {
	switch provider {
	case "mysql":
		return legacyForeignKeyName(table, columns)
	case "sqlite":
		return "fk_" + table + "_" + strings.Join(columns, "_")
	default:
		return DefaultForeignKeyName(table, columns)
	}
}

// legacyForeignKeyName is the comma-joined name older versions generated.
func legacyForeignKeyName(table string, columns []string) string {
	return "fk_" + table + "_" + strings.Join(columns, ",")
}

// PostgresPrimaryKeyName is the name PostgreSQL assigns to an unnamed
// primary key constraint.
func PostgresPrimaryKeyName(table string) string {
	return postgresMakeObjectName(table, "", "pkey")
}

// PostgresUniqueKeyName is the name PostgreSQL assigns to an unnamed unique
// constraint over columns.
func PostgresUniqueKeyName(table string, columns []string) string {
	return postgresMakeObjectName(table, postgresIndexNameAddition(columns), "key")
}

// postgresMakeObjectName mirrors PostgreSQL's makeObjectName: the longer of
// name1 and name2 is truncated first so that name1_name2_label fits in
// MaxIdentifierBytes, cutting each part at a character boundary.
func postgresMakeObjectName(name1, name2, label string) string {
	overhead := 0
	if name2 != "" {
		overhead++
	}
	if label != "" {
		overhead += len(label) + 1
	}
	available := MaxIdentifierBytes - overhead
	name1Bytes, name2Bytes := len(name1), len(name2)
	for name1Bytes+name2Bytes > available {
		if name1Bytes > name2Bytes {
			name1Bytes--
		} else {
			name2Bytes--
		}
	}
	name := clipUTF8(name1, name1Bytes)
	if name2 != "" {
		name += "_" + clipUTF8(name2, name2Bytes)
	}
	if label != "" {
		name += "_" + label
	}
	return name
}

// postgresIndexNameAddition mirrors PostgreSQL's ChooseIndexNameAddition,
// which stops appending column names once the result reaches NAMEDATALEN.
func postgresIndexNameAddition(columns []string) string {
	var b strings.Builder
	for _, column := range columns {
		if b.Len() > 0 {
			b.WriteByte('_')
		}
		b.WriteString(clipUTF8(column, MaxIdentifierBytes))
		if b.Len() > MaxIdentifierBytes {
			break
		}
	}
	return b.String()
}

// PostgresTruncatedIdentifier is the name PostgreSQL stores for an
// identifier: names longer than MaxIdentifierBytes are cut at a character
// boundary.
func PostgresTruncatedIdentifier(name string) string {
	return clipUTF8(name, MaxIdentifierBytes)
}

// IndexNames returns the name migrations give an index of model and the
// full name it is derived from: the explicit name, or the generated name
// before LimitIdentifier. Older versions created indexes under the full name,
// which PostgreSQL truncated when it exceeded MaxIdentifierBytes.
func IndexNames(model *ir.Model, idx *ir.Index) (name, full string) {
	name = effectiveIndexName("postgresql", model, idx)
	if idx == nil || idx.Name != "" {
		return name, name
	}
	prefix := "idx_"
	if idx.IsUnique {
		prefix = "uq_"
	}
	table := ""
	if model != nil {
		table = model.TableName()
	}
	return name, prefix + table + "_" + strings.Join(normalizeFieldNames(model, idx.Fields), "_")
}
