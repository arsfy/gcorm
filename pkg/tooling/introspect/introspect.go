// Package introspect implements the `gco introspect` command for generating
// schema files from an existing database.
package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/dbpush"
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
	Name        string
	Columns     []string
	Expressions []string
	IsUnique    bool
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

	normalized, err := dbpush.NormalizeConnectionURL(dsn, provider)
	if err != nil {
		return err
	}
	db, err := sql.Open(introspectDriver(provider), normalized)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	schema, err := dbpush.IntrospectDatabase(ctx, db, provider, nil, normalized)
	if err != nil {
		return fmt.Errorf("introspect database: %w", err)
	}
	content := GenerateSchema(provider, tablesFromIR(schema))
	if compiled := compiler.CompileFile("schema.gcorm", []byte(content)); compiled.HasErrors() {
		return fmt.Errorf("generated schema is invalid: %v", compiled.Errors[0])
	}
	if err := WriteSchemaFile(output, "schema.gcorm", content); err != nil {
		return err
	}
	fmt.Printf("Introspected %d table(s) into %s\n", len(schema.Models), filepath.Join(output, "schema.gcorm"))
	return nil
}

func introspectDriver(provider string) string {
	if provider == "postgresql" {
		return "pgx"
	}
	if provider == "sqlite" {
		return "sqlite"
	}
	return provider
}

func tablesFromIR(schema *ir.Schema) []TableInfo {
	if schema == nil {
		return nil
	}
	out := make([]TableInfo, 0, len(schema.Models))
	for _, model := range schema.Models {
		t := TableInfo{Name: model.TableName(), Schema: model.Schema}
		pk := map[string]bool{}
		if model.PrimaryKey != nil {
			for _, f := range model.PrimaryKey.Fields {
				pk[f] = true
			}
		}
		for _, f := range model.ScalarFields() {
			name := f.DBName
			if name == "" {
				name = f.Name
			}
			c := ColumnInfo{Name: name, DataType: f.ScalarType, IsNullable: f.IsOptional, IsPrimaryKey: pk[f.Name] || pk[name] || f.IsID, IsUnique: f.IsUnique}
			if f.Default != nil {
				if f.Default.IsFunction {
					c.Default = f.Default.FuncName + "()"
				} else {
					c.Default = f.Default.Value
				}
			}
			t.Columns = append(t.Columns, c)
		}
		for _, idx := range model.Indexes {
			info := IndexInfo{Name: idx.Name, Columns: append([]string(nil), idx.Fields...), IsUnique: idx.IsUnique}
			if len(idx.Columns) > 0 {
				info.Columns = make([]string, len(idx.Columns))
				info.Expressions = make([]string, len(idx.Columns))
				for i, col := range idx.Columns {
					info.Columns[i] = col.Field
					info.Expressions[i] = col.Expression
				}
			}
			t.Indexes = append(t.Indexes, info)
		}
		for _, uc := range model.UniqueConstraints {
			t.Indexes = append(t.Indexes, IndexInfo{Name: uc.Name, Columns: append([]string(nil), uc.Fields...), IsUnique: true})
		}
		for _, rel := range model.Relations {
			if len(rel.Fields) > 0 {
				t.FKs = append(t.FKs, ForeignKeyInfo{Name: rel.ConstraintName, Columns: append([]string(nil), rel.Fields...), RefTable: rel.ToModel, RefColumns: append([]string(nil), rel.References...), OnDelete: rel.OnDelete, OnUpdate: rel.OnUpdate})
			}
		}
		out = append(out, t)
	}
	return out
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

	modelNames := make(map[string]string)
	usedModels := make(map[string]bool)
	fieldNames := make(map[string]map[string]string)
	for _, t := range tables {
		modelNames[t.Name] = uniqueName(sanitizeModelName(t.Name), usedModels)
		used := make(map[string]bool)
		fieldNames[t.Name] = make(map[string]string)
		for _, col := range t.Columns {
			fieldNames[t.Name][col.Name] = uniqueName(sanitizeFieldName(col.Name), used)
		}
	}

	for _, t := range tables {
		modelName := modelNames[t.Name]
		b.WriteString(fmt.Sprintf("\nmodel %s {\n", modelName))
		pkCount := 0
		for _, col := range t.Columns {
			if col.IsPrimaryKey {
				pkCount++
			}
		}
		for _, col := range t.Columns {
			fieldType := mapSQLType(provider, col.DataType)
			fieldName := fieldNames[t.Name][col.Name]
			line := fmt.Sprintf("  %-20s %s", fieldName, fieldType)
			if col.IsNullable {
				line += "?"
			}
			if col.IsPrimaryKey && pkCount == 1 {
				line += " @id"
			}
			if col.IsUnique && !col.IsPrimaryKey {
				line += " @unique"
			}
			if col.Default != "" {
				line += fmt.Sprintf(" @default(%s)", mapDefaultForType(col.Default, fieldType))
			}
			if fieldName != col.Name {
				line += fmt.Sprintf(" @map(%q)", col.Name)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		for i, fk := range t.FKs {
			refModel := modelNames[fk.RefTable]
			if refModel == "" {
				refModel = sanitizeModelName(fk.RefTable)
			}
			name := uniqueName(sanitizeFieldName(fk.RefTable), mapUsedValues(fieldNames[t.Name]))
			local := mappedNames(fieldNames[t.Name], fk.Columns)
			refs := mappedNames(fieldNames[fk.RefTable], fk.RefColumns)
			if len(local) == 0 || len(local) != len(refs) {
				continue
			}
			line := fmt.Sprintf("  %-20s %s @relation(fields: [%s], references: [%s]", name, refModel, strings.Join(local, ", "), strings.Join(refs, ", "))
			if fk.Name != "" {
				line += fmt.Sprintf(", name: %q", fk.Name)
			}
			if fk.OnDelete != "" {
				line += ", onDelete: " + referentialAction(fk.OnDelete)
			}
			if fk.OnUpdate != "" {
				line += ", onUpdate: " + referentialAction(fk.OnUpdate)
			}
			line += ")"
			if i >= 0 {
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
		if pkCount > 1 {
			var cols []string
			for _, col := range t.Columns {
				if col.IsPrimaryKey {
					cols = append(cols, fieldNames[t.Name][col.Name])
				}
			}
			fmt.Fprintf(&b, "\n  @@id([%s])\n", strings.Join(cols, ", "))
		}

		// Model-level indexes.
		for _, idx := range t.Indexes {
			cols := indexSchemaFields(t, idx, fieldNames[t.Name])
			if len(cols) != len(idx.Columns) {
				fmt.Fprintf(&b, "\n  // WARNING: index %q omitted because its columns could not be represented\n", idx.Name)
				continue
			}
			kind := "index"
			if idx.IsUnique {
				kind = "unique"
			}
			args := []string{"[" + strings.Join(cols, ", ") + "]"}
			if idx.Name != "" {
				args = append(args, fmt.Sprintf("name: %q", idx.Name))
			}
			if hasIndexExpressions(idx.Expressions) {
				args = append(args, formatIndexExpressions(idx.Expressions))
			}
			fmt.Fprintf(&b, "\n  @@%s(%s)\n", kind, strings.Join(args, ", "))
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

func indexSchemaFields(table TableInfo, idx IndexInfo, mapping map[string]string) []string {
	out := make([]string, 0, len(idx.Columns))
	for i, column := range idx.Columns {
		if name := mapping[column]; name != "" {
			out = append(out, name)
			continue
		}
		expression := ""
		if i < len(idx.Expressions) {
			expression = idx.Expressions[i]
		}
		for _, candidate := range table.Columns {
			if postgresExpressionReferencesColumn(expression, candidate.Name) {
				out = append(out, mapping[candidate.Name])
				break
			}
		}
		if len(out) == i && strings.TrimSpace(expression) != "" && len(table.Columns) > 0 {
			out = append(out, mapping[table.Columns[0].Name])
		}
	}
	return out
}

func postgresExpressionReferencesColumn(expression, column string) bool {
	for _, candidate := range []string{column, `"` + strings.ReplaceAll(column, `"`, `""`) + `"`} {
		for start := 0; ; {
			i := strings.Index(expression[start:], candidate)
			if i < 0 {
				break
			}
			i += start
			beforeOK := i == 0 || !isIdentifierByte(expression[i-1])
			end := i + len(candidate)
			afterOK := end == len(expression) || !isIdentifierByte(expression[end])
			if beforeOK && afterOK {
				return true
			}
			start = end
		}
	}
	return false
}

func isIdentifierByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func hasIndexExpressions(expressions []string) bool {
	for _, expression := range expressions {
		if strings.TrimSpace(expression) != "" {
			return true
		}
	}
	return false
}

func formatIndexExpressions(expressions []string) string {
	if len(expressions) == 1 {
		return fmt.Sprintf("expression: %q", expressions[0])
	}
	values := make([]string, len(expressions))
	for i, expression := range expressions {
		values[i] = fmt.Sprintf("%q", expression)
	}
	return "expressions: [" + strings.Join(values, ", ") + "]"
}

func sanitizeModelName(name string) string {
	value := sanitizeIdentifier(pascalCase(name), true)
	if value == "" {
		return "Model"
	}
	return value
}
func sanitizeFieldName(name string) string {
	value := sanitizeIdentifier(name, false)
	if value == "" {
		return "field"
	}
	return value
}
func sanitizeIdentifier(name string, upper bool) string {
	var b strings.Builder
	capitalize := upper
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if b.Len() == 0 && unicode.IsDigit(r) {
				if upper {
					b.WriteString("Model")
				} else {
					b.WriteString("field")
				}
			}
			if capitalize {
				r = unicode.ToUpper(r)
				capitalize = false
			}
			b.WriteRune(r)
		} else {
			capitalize = true
		}
	}
	value := b.String()
	if !upper && value != "" {
		rs := []rune(value)
		rs[0] = unicode.ToLower(rs[0])
		value = string(rs)
	}
	return value
}
func uniqueName(base string, used map[string]bool) string {
	name := base
	for i := 2; used[name]; i++ {
		name = base + strconv.Itoa(i)
	}
	used[name] = true
	return name
}
func mappedNames(mapping map[string]string, names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if v := mapping[n]; v != "" {
			out = append(out, v)
		}
	}
	return out
}
func mapUsedValues(mapping map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, v := range mapping {
		out[v] = true
	}
	return out
}
func referentialAction(value string) string {
	switch strings.ToUpper(strings.ReplaceAll(value, " ", "")) {
	case "CASCADE":
		return "Cascade"
	case "RESTRICT":
		return "Restrict"
	case "SETNULL":
		return "SetNull"
	case "SETDEFAULT":
		return "SetDefault"
	default:
		return "NoAction"
	}
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
