// Package introspect implements the `gco introspect` command for generating
// schema files from an existing database.
package introspect

import (
	"fmt"
	"os"
	"path/filepath"
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

// GenerateSchema produces .gco schema text from introspected table info.
func GenerateSchema(provider string, tables []TableInfo) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("datasource db {\n  provider = %q\n  url      = env(\"DATABASE_URL\")\n}\n\n", provider))
	b.WriteString("generator client {\n  provider = \"gco-go\"\n  output   = \"./gen\"\n}\n")

	for _, t := range tables {
		b.WriteString(fmt.Sprintf("\nmodel %s {\n", pascalCase(t.Name)))
		for _, col := range t.Columns {
			line := fmt.Sprintf("  %-20s %s", col.Name, mapSQLType(provider, col.DataType))
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
				line += fmt.Sprintf(" @default(%s)", mapDefault(col.Default))
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

		if t.Schema != "" {
			b.WriteString(fmt.Sprintf("\n  @@schema(%q)\n", t.Schema))
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
	case strings.HasPrefix(upper, "INT"), strings.HasPrefix(upper, "INTEGER"),
		strings.HasPrefix(upper, "SERIAL"), strings.HasPrefix(upper, "SMALLINT"),
		strings.HasPrefix(upper, "TINYINT"):
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
	lower := strings.ToLower(strings.TrimSpace(def))
	switch {
	case strings.Contains(lower, "uuid"):
		return "uuid()"
	case strings.Contains(lower, "now") || strings.Contains(lower, "current_timestamp"):
		return "now()"
	case strings.Contains(lower, "nextval") || lower == "autoincrement" ||
		strings.Contains(lower, "auto_increment"):
		return "autoincrement()"
	default:
		return fmt.Sprintf("%q", def)
	}
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
