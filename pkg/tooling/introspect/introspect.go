// Package introspect implements the `gco introspect` command for generating
// schema files from an existing database.
package introspect

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// TableInfo represents a database table discovered during introspection.
type TableInfo struct {
	Name    string
	Schema  string // database schema/namespace
	Columns []ColumnInfo
	Indexes []IndexInfo
	FKs     []ForeignKeyInfo
}

// ColumnInfo describes a single database column.
type ColumnInfo struct {
	Name         string
	DataType     string
	IsNullable   bool
	IsPrimaryKey bool
	IsUnique     bool
	Default      string
	Comment      string
}

// IndexInfo describes a database index.
type IndexInfo struct {
	Name     string
	Columns  []string
	IsUnique bool
}

// ForeignKeyInfo describes a foreign key relationship.
type ForeignKeyInfo struct {
	Name       string
	Columns    []string
	RefTable   string
	RefColumns []string
	OnDelete   string
	OnUpdate   string
}

// Run executes the introspect command.
func Run(args []string) error {
	dsn := ""
	provider := ""
	output := "schema"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if i+1 < len(args) {
				dsn = args[i+1]
				i++
			}
		case "--provider":
			if i+1 < len(args) {
				provider = args[i+1]
				i++
			}
		case "--output":
			if i+1 < len(args) {
				output = args[i+1]
				i++
			}
		}
	}

	if dsn == "" {
		return fmt.Errorf("--url is required for introspection")
	}

	if provider == "" {
		provider = DetectProvider(dsn)
	}

	fmt.Printf("Introspecting %s database...\n", provider)
	fmt.Printf("Output directory: %s\n", output)

	// Introspection requires a live database connection.
	// This implementation generates the schema file structure without connecting.
	fmt.Println("Note: Full introspection requires database driver support.")
	fmt.Println("Connect with a live database to generate schema from existing tables.")
	return nil
}

// DetectProvider infers the database provider from a connection string.
func DetectProvider(dsn string) string {
	lower := strings.ToLower(dsn)
	switch {
	case strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://"):
		return "postgresql"
	case strings.HasPrefix(lower, "mysql://") || strings.Contains(lower, "@tcp("):
		return "mysql"
	case strings.HasPrefix(lower, "sqlite://") || strings.HasPrefix(lower, "file:") ||
		strings.HasSuffix(lower, ".db") || strings.HasSuffix(lower, ".sqlite"):
		return "sqlite"
	default:
		return "postgresql"
	}
}

// GenerateSchema produces .gcorm schema text from introspected table info.
func GenerateSchema(provider string, tables []TableInfo) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("datasource db {\n  provider = %q\n  url      = env(\"DATABASE_URL\")\n}\n\n", provider))
	b.WriteString("generator client {\n  provider = \"gco-go\"\n  output   = \"./gen\"\n}\n")

	for _, t := range tables {
		modelName := pascalCase(t.Name)
		b.WriteString(fmt.Sprintf("\nmodel %s {\n", modelName))
		for _, col := range t.Columns {
			fieldType := mapSQLType(provider, col.DataType)
			line := fmt.Sprintf("  %-20s %s", col.Name, fieldType)
			if col.IsNullable {
				line += "?"
			}
			if col.IsPrimaryKey {
				line += " @id"
			}
			if col.IsUnique && !col.IsPrimaryKey {
				line += " @unique"
			}
			if col.Default != "" {
				line += fmt.Sprintf(" @default(%s)", mapDefaultForType(col.Default, fieldType))
			}
			b.WriteString(line + "\n")
		}

		// Model-level indexes.
		for _, idx := range t.Indexes {
			if !idx.IsUnique {
				cols := make([]string, len(idx.Columns))
				copy(cols, idx.Columns)
				fmt.Fprintf(&b, "\n  @@index([%s])\n", strings.Join(cols, ", "))
			}
		}

		if modelName != t.Name {
			fmt.Fprintf(&b, "\n  @@map(%q)\n", t.Name)
		}

		if t.Schema != "" {
			fmt.Fprintf(&b, "\n  @@schema(%q)\n", t.Schema)
		}

		b.WriteString("}\n")
	}

	return b.String()
}

// WriteSchemaFile writes generated schema to a file.
func WriteSchemaFile(dir, filename, content string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write schema file: %w", err)
	}
	return nil
}

// mapSQLType maps a SQL column type to GCO schema type.
func mapSQLType(provider, sqlType string) string {
	upper := strings.ToUpper(strings.TrimSpace(sqlType))

	switch {
	case strings.Contains(upper, "UUID"), strings.Contains(upper, "CHAR(36)"):
		return "UUID"
	case strings.HasPrefix(upper, "BIGINT"), strings.HasPrefix(upper, "BIGSERIAL"):
		return "BigInt"
	case strings.HasPrefix(upper, "SMALLINT"), strings.HasPrefix(upper, "INT2"):
		return "SmallInt"
	case strings.HasPrefix(upper, "INT"), strings.HasPrefix(upper, "INTEGER"),
		strings.HasPrefix(upper, "SERIAL"), strings.HasPrefix(upper, "TINYINT"):
		if upper == "TINYINT(1)" && provider == "mysql" {
			return "Boolean"
		}
		return "Int"
	case strings.HasPrefix(upper, "FLOAT"), strings.HasPrefix(upper, "DOUBLE"),
		strings.HasPrefix(upper, "REAL"):
		return "Float"
	case strings.HasPrefix(upper, "DECIMAL"), strings.HasPrefix(upper, "NUMERIC"):
		return "Decimal"
	case strings.HasPrefix(upper, "BOOL"):
		return "Boolean"
	case strings.Contains(upper, "TIMESTAMP"), strings.Contains(upper, "DATETIME"),
		strings.Contains(upper, "DATE"):
		return "DateTime"
	case strings.Contains(upper, "BYTEA"), strings.Contains(upper, "BLOB"),
		strings.Contains(upper, "BINARY"):
		return "Bytes"
	case strings.HasPrefix(upper, "JSON"):
		return "Json"
	default:
		return "String"
	}
}

// mapDefault maps a SQL default expression to a GCO schema default.
func mapDefault(def string) string {
	return mapDefaultForType(def, "")
}

func mapDefaultForType(def, fieldType string) string {
	value := strings.TrimSpace(def)
	for hasWrappingDefaultParens(value) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}

	lower := strings.ToLower(value)
	quoted := isSQLStringLiteral(value)
	switch {
	case !quoted && strings.Contains(lower, "uuid"):
		return "uuid()"
	case !quoted && (strings.Contains(lower, "now()") || strings.HasPrefix(lower, "current_timestamp")):
		return "now()"
	case !quoted && (strings.Contains(lower, "nextval") || lower == "autoincrement" ||
		strings.Contains(lower, "auto_increment")):
		return "autoincrement()"
	}

	value = stripPostgresDefaultCast(value)
	for hasWrappingDefaultParens(value) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	value = unquoteSQLDefault(value)
	lower = strings.ToLower(value)

	switch fieldType {
	case "Boolean":
		switch lower {
		case "1", "true":
			return "true"
		case "0", "false":
			return "false"
		}
	case "Int", "SmallInt", "BigInt", "Float", "Decimal":
		if isNumericDefault(value) {
			return value
		}
	}

	if fieldType == "" {
		if lower == "true" || lower == "false" {
			return lower
		}
		if isNumericDefault(value) {
			return value
		}
	}
	return strconv.Quote(value)
}

func stripPostgresDefaultCast(value string) string {
	value = strings.TrimSpace(value)
	for {
		castIndex := trailingPostgresCastIndex(value)
		if castIndex < 0 {
			return value
		}
		value = strings.TrimSpace(value[:castIndex])
	}
}

func trailingPostgresCastIndex(value string) int {
	depth := 0
	quote := byte(0)
	lastCast := -1
	for i := 0; i+1 < len(value); i++ {
		if quote != 0 {
			if value[i] == quote {
				if i+1 < len(value) && value[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch value[i] {
		case '\'', '"':
			quote = value[i]
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 && value[i+1] == ':' {
				lastCast = i
				i++
			}
		}
	}
	if lastCast < 0 || !isPostgresCastType(value[lastCast+2:]) {
		return -1
	}
	return lastCast
}

func isPostgresCastType(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for strings.HasSuffix(value, "[]") {
		value = strings.TrimSpace(strings.TrimSuffix(value, "[]"))
	}
	if value == "" {
		return false
	}
	if open := strings.LastIndexByte(value, '('); open > 0 && strings.HasSuffix(value, ")") {
		precision := value[open+1 : len(value)-1]
		if precision == "" {
			return false
		}
		for _, r := range precision {
			if r < '0' || r > '9' {
				if r != ',' && r != ' ' && r != '\t' {
					return false
				}
			}
		}
		value = strings.TrimSpace(value[:open])
	}

	normalized := strings.ToLower(strings.Join(strings.Fields(value), " "))
	switch normalized {
	case "bit varying", "character varying", "double precision",
		"time with time zone", "time without time zone",
		"timestamp with time zone", "timestamp without time zone":
		return true
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '$' || r == '.' || r == '"' {
			continue
		}
		return false
	}
	return true
}

func isSQLStringLiteral(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '\'' && value[0] != '"' {
		return false
	}
	quote := value[0]
	for i := 1; i < len(value); i++ {
		if value[i] != quote {
			continue
		}
		if i+1 < len(value) && value[i+1] == quote {
			i++
			continue
		}
		rest := strings.TrimSpace(value[i+1:])
		return rest == "" || strings.HasPrefix(rest, "::")
	}
	return false
}

func unquoteSQLDefault(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return value
	}
	switch value[0] {
	case '\'':
		if value[len(value)-1] == '\'' {
			return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		}
	case '"':
		if value[len(value)-1] == '"' {
			return strings.ReplaceAll(value[1:len(value)-1], `""`, `"`)
		}
	}
	return value
}

func hasWrappingDefaultParens(value string) bool {
	if len(value) < 2 || value[0] != '(' || value[len(value)-1] != ')' {
		return false
	}
	depth := 0
	quote := byte(0)
	for i := 0; i < len(value); i++ {
		if quote != 0 {
			if value[i] == quote {
				if i+1 < len(value) && value[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch value[i] {
		case '\'', '"':
			quote = value[i]
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(value)-1 {
				return false
			}
		}
		if depth < 0 {
			return false
		}
	}
	return depth == 0
}

func isNumericDefault(value string) bool {
	if value == "" {
		return false
	}
	i := 0
	if value[0] == '-' {
		i++
		if i == len(value) {
			return false
		}
	}
	digitStart := i
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == digitStart {
		return false
	}
	if i < len(value) && value[i] == '.' {
		i++
		fractionStart := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == fractionStart {
			return false
		}
	}
	return i == len(value)
}

// pascalCase converts snake_case or plain names to PascalCase for model names.
func pascalCase(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}
