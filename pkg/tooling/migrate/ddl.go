// ddl.go generates dialect-specific DDL SQL from a migration changeset.
package migrate

import (
	"fmt"
	"strings"

	"github.com/arsfy/gco-orm/pkg/schema/ir"
)

// DDLGenerator generates dialect-specific DDL from a changeset.
type DDLGenerator struct {
	Dialect string     // "postgresql", "mysql", "sqlite"
	Schema  *ir.Schema // Target schema for type lookups.
}

// GenerateUp produces the up migration SQL.
func (g DDLGenerator) GenerateUp(cs *Changeset) string {
	var b strings.Builder
	for _, c := range cs.Changes {
		sql := g.changeToUp(c, cs)
		if sql != "" {
			b.WriteString(sql)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// GenerateDown produces the best-effort down migration SQL.
func (g DDLGenerator) GenerateDown(cs *Changeset) string {
	var b strings.Builder
	// Reverse order for down migration.
	for i := len(cs.Changes) - 1; i >= 0; i-- {
		c := cs.Changes[i]
		sql := g.changeToDown(c, cs)
		if sql != "" {
			b.WriteString(sql)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---------------------------------------------------------------------------
// Up SQL generation
// ---------------------------------------------------------------------------

func (g DDLGenerator) changeToUp(c Change, cs *Changeset) string {
	switch c.Type {
	case CreateTable:
		return g.createTableSQL(c, cs)
	case DropTable:
		return fmt.Sprintf("DROP TABLE %s;", g.quoteID(c.Model))
	case AddColumn:
		return g.addColumnSQL(c, cs)
	case DropColumn:
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", g.quoteID(c.Model), g.quoteID(c.Field))
	case AlterType:
		return g.alterTypeSQL(c)
	case AlterNull:
		return g.alterNullSQL(c)
	case AlterDefault:
		return g.alterDefaultSQL(c)
	case AddIndex:
		return g.addIndexSQL(c)
	case DropIndex:
		return g.dropIndexSQL(c)
	case AddUnique:
		return g.addUniqueSQL(c)
	case DropUnique:
		return g.dropUniqueSQL(c)
	case ChangePK:
		return g.changePKSQL(c)
	case AddFK:
		return g.addFKSQL(c)
	case DropFK:
		return g.dropFKSQL(c)
	default:
		return fmt.Sprintf("-- unsupported change type: %s", c.Type)
	}
}

func (g DDLGenerator) createTableSQL(c Change, cs *Changeset) string {
	model := findModel(cs.New, c.Model)
	if model == nil {
		return fmt.Sprintf("-- CreateTable %s: model not found in target schema", c.Model)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("CREATE TABLE %s (\n", g.quoteID(model.TableName())))

	var cols []string
	for _, f := range model.ScalarFields() {
		cols = append(cols, "  "+g.columnDef(f))
	}

	if model.PrimaryKey != nil && len(model.PrimaryKey.Fields) > 0 {
		quoted := g.quoteIDs(model.PrimaryKey.Fields)
		cols = append(cols, fmt.Sprintf("  PRIMARY KEY (%s)", strings.Join(quoted, ", ")))
	}

	b.WriteString(strings.Join(cols, ",\n"))
	b.WriteString("\n);")
	return b.String()
}

func (g DDLGenerator) addColumnSQL(c Change, cs *Changeset) string {
	f := findField(cs.New, c.Model, c.Field)
	if f == nil {
		return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;",
			g.quoteID(c.Model), g.quoteID(c.Field), g.sqlType(c.NewValue))
	}
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;",
		g.quoteID(c.Model), g.columnDef(f))
}

func (g DDLGenerator) alterTypeSQL(c Change) string {
	tbl := g.quoteID(c.Model)
	col := g.quoteID(c.Field)
	newType := g.sqlType(c.NewValue)

	switch g.Dialect {
	case "postgresql":
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", tbl, col, newType)
	case "mysql":
		return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s;", tbl, col, newType)
	default: // sqlite
		return fmt.Sprintf("-- SQLite: ALTER COLUMN not supported; table rebuild required for %s.%s to %s", c.Model, c.Field, newType)
	}
}

func (g DDLGenerator) alterNullSQL(c Change) string {
	tbl := g.quoteID(c.Model)
	col := g.quoteID(c.Field)

	nullable := c.NewValue == "optional"

	switch g.Dialect {
	case "postgresql":
		if nullable {
			return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", tbl, col)
		}
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", tbl, col)
	case "mysql":
		sqlType := g.sqlType(c.NewValue)
		// MySQL MODIFY requires restating the full type; use placeholder if we
		// don't know the actual type from the change context.
		if sqlType == "TEXT" {
			sqlType = "/* current_type */"
		}
		if nullable {
			return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s NULL;", tbl, col, sqlType)
		}
		return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s NOT NULL;", tbl, col, sqlType)
	default: // sqlite
		return fmt.Sprintf("-- SQLite: nullability change not supported inline for %s.%s", c.Model, c.Field)
	}
}

func (g DDLGenerator) alterDefaultSQL(c Change) string {
	tbl := g.quoteID(c.Model)
	col := g.quoteID(c.Field)

	if c.NewValue == "" {
		switch g.Dialect {
		case "postgresql":
			return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, col)
		case "mysql":
			return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, col)
		default:
			return fmt.Sprintf("-- SQLite: DROP DEFAULT not supported for %s.%s", c.Model, c.Field)
		}
	}

	switch g.Dialect {
	case "postgresql":
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExpr(c.NewValue))
	case "mysql":
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExpr(c.NewValue))
	default:
		return fmt.Sprintf("-- SQLite: SET DEFAULT not supported inline for %s.%s", c.Model, c.Field)
	}
}

func (g DDLGenerator) addIndexSQL(c Change) string {
	name := c.NewValue
	if name == "" {
		name = fmt.Sprintf("idx_%s_%s", c.Model, c.Details["fields"])
	}
	fields := g.quoteIDs(strings.Split(c.Details["fields"], ","))
	unique := ""
	if c.Details["unique"] == "true" {
		unique = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);",
		unique, g.quoteID(name), g.quoteID(c.Model), strings.Join(fields, ", "))
}

func (g DDLGenerator) dropIndexSQL(c Change) string {
	name := c.OldValue
	if name == "" {
		name = fmt.Sprintf("idx_%s_%s", c.Model, c.Details["fields"])
	}
	switch g.Dialect {
	case "mysql":
		return fmt.Sprintf("DROP INDEX %s ON %s;", g.quoteID(name), g.quoteID(c.Model))
	default:
		return fmt.Sprintf("DROP INDEX %s;", g.quoteID(name))
	}
}

func (g DDLGenerator) addUniqueSQL(c Change) string {
	fields := g.quoteIDs(strings.Split(c.Details["fields"], ","))
	name := c.NewValue
	if name == "" {
		name = fmt.Sprintf("uq_%s_%s", c.Model, c.Details["fields"])
	}
	return fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s);",
		g.quoteID(name), g.quoteID(c.Model), strings.Join(fields, ", "))
}

func (g DDLGenerator) dropUniqueSQL(c Change) string {
	name := c.OldValue
	if name == "" {
		name = fmt.Sprintf("uq_%s_%s", c.Model, c.Details["fields"])
	}
	switch g.Dialect {
	case "mysql":
		return fmt.Sprintf("DROP INDEX %s ON %s;", g.quoteID(name), g.quoteID(c.Model))
	default:
		return fmt.Sprintf("DROP INDEX %s;", g.quoteID(name))
	}
}

func (g DDLGenerator) changePKSQL(c Change) string {
	tbl := g.quoteID(c.Model)
	var parts []string

	if c.OldValue != "" {
		switch g.Dialect {
		case "mysql":
			parts = append(parts, fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY;", tbl))
		default:
			parts = append(parts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;",
				tbl, g.quoteID(c.Model+"_pkey")))
		}
	}

	if c.NewValue != "" {
		fields := g.quoteIDs(strings.Split(c.NewValue, ","))
		parts = append(parts, fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s);",
			tbl, strings.Join(fields, ", ")))
	}

	return strings.Join(parts, "\n")
}

func (g DDLGenerator) addFKSQL(c Change) string {
	tbl := g.quoteID(c.Model)
	localFields := g.quoteIDs(strings.Split(c.Details["fields"], ","))
	refFields := g.quoteIDs(strings.Split(c.Details["references"], ","))
	refTable := g.quoteID(c.Details["toModel"])
	constraintName := g.quoteID(fmt.Sprintf("fk_%s_%s", c.Model, c.Details["fields"]))

	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s);",
		tbl, constraintName, strings.Join(localFields, ", "),
		refTable, strings.Join(refFields, ", "))
}

func (g DDLGenerator) dropFKSQL(c Change) string {
	tbl := g.quoteID(c.Model)
	constraintName := g.quoteID(fmt.Sprintf("fk_%s_%s", c.Model, c.Details["fields"]))

	switch g.Dialect {
	case "mysql":
		return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;", tbl, constraintName)
	default:
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", tbl, constraintName)
	}
}

// ---------------------------------------------------------------------------
// Down SQL generation (reverse operations)
// ---------------------------------------------------------------------------

func (g DDLGenerator) changeToDown(c Change, cs *Changeset) string {
	switch c.Type {
	case CreateTable:
		return fmt.Sprintf("DROP TABLE %s;", g.quoteID(c.Model))
	case DropTable:
		return fmt.Sprintf("-- WARNING: destructive — cannot recreate %s without original schema", c.Model)
	case AddColumn:
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", g.quoteID(c.Model), g.quoteID(c.Field))
	case DropColumn:
		return fmt.Sprintf("-- WARNING: destructive — cannot restore column %s.%s without original definition", c.Model, c.Field)
	case AlterType:
		rev := c
		rev.NewValue = c.OldValue
		return "-- REVIEW REQUIRED: reverting type change\n" + g.alterTypeSQL(rev)
	case AlterNull:
		rev := c
		rev.NewValue = c.OldValue
		return "-- REVIEW REQUIRED: reverting nullability change\n" + g.alterNullSQL(rev)
	case AlterDefault:
		rev := c
		rev.NewValue = c.OldValue
		return g.alterDefaultSQL(rev)
	case AddIndex:
		rev := c
		rev.OldValue = c.NewValue
		return g.dropIndexSQL(rev)
	case DropIndex:
		rev := c
		rev.NewValue = c.OldValue
		return g.addIndexSQL(rev)
	case AddUnique:
		rev := c
		rev.OldValue = c.NewValue
		return g.dropUniqueSQL(rev)
	case DropUnique:
		rev := c
		rev.NewValue = c.OldValue
		return g.addUniqueSQL(rev)
	case ChangePK:
		rev := c
		rev.OldValue = c.NewValue
		rev.NewValue = c.OldValue
		return g.changePKSQL(rev)
	case AddFK:
		rev := c
		return g.dropFKSQL(rev)
	case DropFK:
		rev := c
		return g.addFKSQL(rev)
	default:
		return fmt.Sprintf("-- unsupported reverse for change type: %s", c.Type)
	}
}

// ---------------------------------------------------------------------------
// Quoting helpers
// ---------------------------------------------------------------------------

func (g DDLGenerator) quoteID(name string) string {
	switch g.Dialect {
	case "mysql":
		return "`" + name + "`"
	default: // postgresql, sqlite
		return `"` + name + `"`
	}
}

func (g DDLGenerator) quoteIDs(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = g.quoteID(n)
	}
	return out
}

// ---------------------------------------------------------------------------
// Type mapping
// ---------------------------------------------------------------------------

// sqlType maps a GCO scalar type to the dialect-specific SQL type.
func (g DDLGenerator) sqlType(scalarType string) string {
	pg := map[string]string{
		"String":   "TEXT",
		"Int":      "INTEGER",
		"BigInt":   "BIGINT",
		"Float":    "DOUBLE PRECISION",
		"Decimal":  "DECIMAL(65,30)",
		"Boolean":  "BOOLEAN",
		"DateTime": "TIMESTAMPTZ",
		"Bytes":    "BYTEA",
		"Json":     "JSONB",
		"UUID":     "UUID",
	}
	my := map[string]string{
		"String":   "VARCHAR(255)",
		"Int":      "INT",
		"BigInt":   "BIGINT",
		"Float":    "DOUBLE",
		"Decimal":  "DECIMAL(65,30)",
		"Boolean":  "TINYINT(1)",
		"DateTime": "DATETIME(3)",
		"Bytes":    "LONGBLOB",
		"Json":     "JSON",
		"UUID":     "CHAR(36)",
	}
	sl := map[string]string{
		"String":   "TEXT",
		"Int":      "INTEGER",
		"BigInt":   "INTEGER",
		"Float":    "REAL",
		"Decimal":  "TEXT",
		"Boolean":  "INTEGER",
		"DateTime": "TEXT",
		"Bytes":    "BLOB",
		"Json":     "TEXT",
		"UUID":     "TEXT",
	}

	var m map[string]string
	switch g.Dialect {
	case "postgresql":
		m = pg
	case "mysql":
		m = my
	default:
		m = sl
	}

	if t, ok := m[scalarType]; ok {
		return t
	}
	return "TEXT"
}

// ---------------------------------------------------------------------------
// Column definition builder
// ---------------------------------------------------------------------------

func (g DDLGenerator) columnDef(f *ir.Field) string {
	name := columnName(f)
	var parts []string
	parts = append(parts, g.quoteID(name))
	parts = append(parts, g.sqlType(f.ScalarType))

	if !f.IsOptional {
		parts = append(parts, "NOT NULL")
	}

	if f.Default != nil {
		parts = append(parts, "DEFAULT "+g.defaultExpr(formatDefault(f.Default)))
	}

	return strings.Join(parts, " ")
}

// defaultExpr wraps a default value expression for SQL.
func (g DDLGenerator) defaultExpr(val string) string {
	if val == "" {
		return "''"
	}
	// Function-style defaults → use as-is.
	if strings.Contains(val, "(") {
		return val
	}
	// Bare booleans and numbers pass through.
	if val == "true" || val == "false" {
		return val
	}
	for _, ch := range val {
		if (ch < '0' || ch > '9') && ch != '.' && ch != '-' {
			return "'" + val + "'"
		}
	}
	return val
}
