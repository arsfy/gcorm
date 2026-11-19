// ddl.go generates dialect-specific DDL SQL from a migration changeset.
package migrate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	runtimepkg "github.com/arsfy/gcorm/pkg/runtime"
	runtimedialect "github.com/arsfy/gcorm/pkg/runtime/dialect"
	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// DDLGenerator generates dialect-specific DDL from a changeset.
type DDLGenerator struct {
	Dialect string     // "postgresql", "mysql", "sqlite"
	Schema  *ir.Schema // Target schema for type lookups.
}

// GenerateUp produces the up migration SQL.
func (g DDLGenerator) GenerateUp(cs *Changeset) string {
	steps := g.UpSteps(cs)
	script := make([]string, 0, len(steps)+2)
	for i, step := range steps {
		if step.RequiresCommit && (i == 0 || !steps[i-1].RequiresCommit) {
			script = append(script, requiresCommitStartComment)
		}
		if !step.RequiresCommit && i > 0 && steps[i-1].RequiresCommit {
			script = append(script, requiresCommitEndComment)
		}
		script = append(script, g.scriptSQL(step))
	}
	sql := strings.TrimRight(strings.Join(script, "\n\n"), "\n")
	if g.Dialect == "sqlite" && cs.RebuildsSQLiteTables() {
		return sqliteRebuildScript(sql)
	}
	return sql
}

// DDLStep is a SQL fragment in migration order. SingleStatement marks a
// complete statement whose internal semicolons must be sent to the server.
//
// RequiresCommit marks PostgreSQL enum changes (ALTER TYPE ... ADD VALUE and
// the renames it depends on) that must be committed before later statements
// use the new labels (SQLSTATE 55P04). UpSteps emits all such steps first and
// they depend only on each other, so a caller that applies a migration in a
// transaction runs them in their own committed transaction before the rest.
type DDLStep struct {
	SQL             string
	SingleStatement bool
	RequiresCommit  bool
}

const (
	requiresCommitStartComment = "-- The following enum changes must be committed before later statements use them.\n" +
		"-- Run them outside the transaction that applies the rest of this migration."
	requiresCommitEndComment = "-- End of enum changes that require a separate transaction."
)

// UpSteps returns the SQL for database drivers applying an up migration.
// Callers applying a SQLite rebuild must manage its transaction and PRAGMAs.
func (g DDLGenerator) UpSteps(cs *Changeset) []DDLStep {
	if cs == nil {
		return nil
	}
	var steps []DDLStep
	if g.Dialect == "postgresql" {
		steps = append(steps, g.postgresPreludeSteps(cs)...)
	}
	createdTables := make(map[string]bool)
	for _, c := range cs.Changes {
		if c.Type == CreateTable {
			createdTables[tableKey(c.Schema, c.Model)] = true
		}
	}
	var rebuildTables map[string]bool
	var restore []DDLStep
	if g.Dialect == "sqlite" {
		rebuildTables = sqliteRebuildTables(cs)
		restore = g.sqliteRebuildTriggers(cs, rebuildTables)
	}
	rebuilt := make(map[string]bool)
	for _, c := range g.orderedChanges(cs) {
		switch c.Type {
		case AddTrigger:
			steps = append(steps, g.createTriggerSteps(c.Schema, c.Model, c.Trigger)...)
			continue
		case DropTrigger:
			steps = append(steps, DDLStep{SQL: g.dropTriggerSQL(c.Schema, c.Model, c.Field)})
			continue
		case SetTableSchema:
			if g.Dialect == "postgresql" {
				steps = append(steps, g.setTableSchemaStep(c.Model, c.OldValue, c.NewValue, cs.Old, cs.New))
				continue
			}
		}
		if rebuildTables[c.Model] {
			if !rebuilt[c.Model] {
				steps = append(steps, DDLStep{SQL: g.sqliteRebuildTableSQL(c.Model, cs)})
				rebuilt[c.Model] = true
			}
			continue
		}
		if g.Dialect == "sqlite" && c.Type == AddFK && createdTables[tableKey(c.Schema, c.Model)] {
			continue
		}
		if sql := g.changeToUp(c, cs); sql != "" {
			steps = append(steps, DDLStep{SQL: sql})
		}
	}
	return append(steps, restore...)
}

// enumDBName returns the database name of an enum type, honoring @@map.
// It falls back to the given name when the enum is not in the schema.
func enumDBName(schema *ir.Schema, name string) string {
	if e := findEnum(schema, name); e != nil {
		return enumTypeName(e)
	}
	return name
}

// RebuildsSQLiteTables reports whether the changes need SQLite's copy-and-swap
// procedure when applied to an existing schema.
func (cs *Changeset) RebuildsSQLiteTables() bool {
	return len(sqliteRebuildTables(cs)) > 0
}

func sqliteRebuildTables(cs *Changeset) map[string]bool {
	tables := make(map[string]bool)
	if cs == nil {
		return tables
	}
	for _, c := range cs.Changes {
		if sqliteRequiresRebuild(c) {
			tables[c.Model] = true
		}
	}
	for _, c := range cs.Changes {
		if c.Type == CreateTable || c.Type == DropTable {
			delete(tables, c.Model)
		}
	}
	return tables
}

func sqliteRequiresRebuild(c Change) bool {
	switch c.Type {
	case DropColumn, AlterType, AlterNull, AlterDefault, AddUnique, DropUnique, ChangePK, AddFK, DropFK:
		return true
	case AddColumn:
		// SQLite only accepts constant defaults in ALTER TABLE ADD COLUMN.
		return c.NewField != nil && c.NewField.Default != nil && c.NewField.Default.IsFunction
	}
	return false
}

func (g DDLGenerator) sqliteRebuildTableSQL(table string, cs *Changeset) string {
	newModel := findModel(g.Schema, table)
	if newModel == nil {
		return "-- SQLite table rebuild unavailable for " + table
	}
	temp := "__gco_new_" + table
	create := g.createTableDefinitionSQL(newModel, "", temp, cs.New)
	oldModel := findModel(cs.Old, table)
	oldCols := map[string]bool{}
	if oldModel != nil {
		for _, f := range oldModel.ScalarFields() {
			oldCols[columnName(f)] = true
		}
	}
	var cols []string
	for _, f := range newModel.ScalarFields() {
		col := columnName(f)
		if oldCols[col] && !f.IsGenerated {
			cols = append(cols, col)
		}
	}
	var b strings.Builder
	b.WriteString(create)
	b.WriteString("\n")
	if len(cols) > 0 {
		quoted := g.quoteIDs(cols)
		fmt.Fprintf(&b, "INSERT INTO %s (%s) SELECT %s FROM %s;\n", g.quoteID(temp), strings.Join(quoted, ", "), strings.Join(quoted, ", "), g.quoteID(table))
	} else if oldModel != nil {
		// Even if every column changed, copy one row per original row.
		// Required fields without defaults cause the copy to fail.
		var defaults []string
		for _, f := range newModel.ScalarFields() {
			if f.IsGenerated {
				continue
			}
			cols = append(cols, columnName(f))
			value := "NULL"
			if g.hasDatabaseDefault(f) {
				value = g.defaultExprForField(f)
			}
			defaults = append(defaults, value)
		}
		fmt.Fprintf(&b, "INSERT INTO %s (%s) SELECT %s FROM %s;\n", g.quoteID(temp), strings.Join(g.quoteIDs(cols), ", "), strings.Join(defaults, ", "), g.quoteID(table))
	}
	if autoColumn := g.sqliteInlineAutoIncrementPrimaryKeyColumn(oldModel); autoColumn != "" &&
		autoColumn == g.sqliteInlineAutoIncrementPrimaryKeyColumn(newModel) {
		oldName, newName := quoteSQLString(table), quoteSQLString(temp)
		fmt.Fprintf(&b, "UPDATE main.sqlite_sequence SET seq = max(seq, coalesce((SELECT seq FROM main.sqlite_sequence WHERE name = %s), seq)) WHERE name = %s;\n", oldName, newName)
		fmt.Fprintf(&b, "INSERT INTO main.sqlite_sequence (name, seq) SELECT %s, seq FROM main.sqlite_sequence WHERE name = %s AND NOT EXISTS (SELECT 1 FROM main.sqlite_sequence WHERE name = %s);\n", newName, oldName, newName)
	}
	fmt.Fprintf(&b, "DROP TABLE %s;\nALTER TABLE %s RENAME TO %s;", g.quoteID(table), g.quoteID(temp), g.quoteID(table))
	for _, idx := range newModel.Indexes {
		name := effectiveIndexName(g.Dialect, newModel, idx)
		unique := ""
		if idx.IsUnique {
			unique = "UNIQUE "
		}
		b.WriteString("\n")
		b.WriteString(g.createIndexSQL(name, "", table, unique, g.indexColumnsSQL(newModel, idx), idx.Where, indexHasExpressions(idx)))
	}
	return b.String()
}

// GenerateDown produces the best-effort down migration SQL.
func (g DDLGenerator) GenerateDown(cs *Changeset) string {
	if g.Dialect == "sqlite" && cs != nil && cs.Old != nil && cs.New != nil {
		reverse := Diff(cs.New, cs.Old)
		if cs.RebuildsSQLiteTables() || reverse.RebuildsSQLiteTables() {
			g.Schema = cs.Old
			sql := g.GenerateUp(reverse)
			if !reverse.RebuildsSQLiteTables() {
				sql = sqliteRebuildScript(sql)
			}
			return "-- REVIEW REQUIRED: restores the previous schema. Review data lost by dropped columns or tables.\n" + sql
		}
	}
	var b strings.Builder
	ordered := g.orderedChanges(cs)
	for i := len(ordered) - 1; i >= 0; i-- {
		c := ordered[i]
		sql := g.changeToDown(c, cs)
		if sql != "" {
			b.WriteString(sql)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func sqliteRebuildScript(sql string) string {
	return `PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;
PRAGMA legacy_alter_table = ON;

` + sql + `

CREATE TEMP TABLE "__gco_fk_check" (ok INTEGER CONSTRAINT foreign_key_check CHECK (ok));
INSERT OR ROLLBACK INTO temp."__gco_fk_check" SELECT 0 FROM pragma_foreign_key_check LIMIT 1;
DROP TABLE temp."__gco_fk_check";
COMMIT;
PRAGMA legacy_alter_table = OFF;
PRAGMA foreign_keys = ON;`
}

// ---------------------------------------------------------------------------
// Up SQL generation
// ---------------------------------------------------------------------------

func (g DDLGenerator) changeToUp(c Change, cs *Changeset) string {
	switch c.Type {
	case CreateTable:
		return g.createTableSQL(c, cs)
	case DropTable:
		return fmt.Sprintf("DROP TABLE %s;", g.tableRef(c.Schema, c.Model))
	case SetTableSchema:
		return fmt.Sprintf("-- %s: schemas are not supported; cannot move table %s", strings.ToUpper(g.Dialect), c.Model)
	case AddColumn:
		return g.addColumnSQL(c, cs)
	case DropColumn:
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", g.tableRef(c.Schema, c.Model), g.quoteID(c.Field))
	case AlterType:
		return g.alterTypeSQL(c)
	case AlterNull:
		return g.alterNullSQL(c)
	case AlterDefault:
		if g.Dialect == "postgresql" && postgresAlterTypeSetsDefault(cs, c) {
			return ""
		}
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
	case CreateEnum, RenameEnum:
		return "" // PostgreSQL enum types are created and renamed before table changes.
	case AlterEnum:
		if g.Dialect == "mysql" {
			return g.mysqlRefreshEnumColumns(c.Model)
		}
		return "" // PostgreSQL enum changes are emitted in postgresPreludeSteps; SQLite stores enum values as text.
	case DropEnum:
		if g.Dialect == "postgresql" {
			return "DROP TYPE " + g.quoteID(enumDBName(cs.Old, c.Model)) + ";"
		}
		return ""
	default:
		return fmt.Sprintf("-- unsupported change type: %s", c.Type)
	}
}

func (g DDLGenerator) mysqlRefreshEnumColumns(enumName string) string {
	return g.mysqlRefreshEnumColumnsFrom(g.Schema, enumName)
}

// mysqlRefreshEnumColumnsFrom re-emits MODIFY COLUMN for every column using
// the named enum, taking the enum values from the given schema. The column
// definition is built without an inline UNIQUE clause: repeated MODIFY
// statements would otherwise accumulate duplicate unique indexes.
func (g DDLGenerator) mysqlRefreshEnumColumnsFrom(schema *ir.Schema, enumName string) string {
	if schema == nil {
		return ""
	}
	enumType := "TEXT"
	if enum := findEnum(schema, enumName); enum != nil {
		values := make([]string, len(enum.Values))
		for i, value := range enum.Values {
			values[i] = quoteSQLString(enumValueLabel(value))
		}
		enumType = "ENUM(" + strings.Join(values, ", ") + ")"
	}
	g.Schema = schema
	var statements []string
	for _, m := range schema.Models {
		for _, f := range m.ScalarFields() {
			if f.Type != ir.FieldKindEnum || f.EnumType != enumName {
				continue
			}
			parts := []string{g.quoteID(columnName(f)), enumType}
			if !f.IsOptional {
				parts = append(parts, "NOT NULL")
			}
			if g.hasDatabaseDefault(f) {
				parts = append(parts, "DEFAULT "+g.defaultExprForField(f))
			}
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", g.quoteID(m.TableName()), strings.Join(parts, " ")))
		}
	}
	return strings.Join(statements, "\n")
}

func (g DDLGenerator) createTableSQL(c Change, cs *Changeset) string {
	model := findTableModel(cs.New, c.Schema, c.Model)
	if model == nil {
		return fmt.Sprintf("-- CreateTable %s: model not found in target schema", c.Model)
	}

	var b strings.Builder
	b.WriteString(g.createTableDefinitionSQL(model, model.Schema, model.TableName(), cs.New))
	for _, idx := range model.Indexes {
		name := effectiveIndexName(g.Dialect, model, idx)
		unique := ""
		if idx.IsUnique {
			unique = "UNIQUE "
		}
		b.WriteString("\n")
		b.WriteString(g.createIndexSQL(name, model.Schema, model.TableName(), unique, g.indexColumnsSQL(model, idx), idx.Where, indexHasExpressions(idx)))
	}
	return b.String()
}

func (g DDLGenerator) createTableDefinitionSQL(model *ir.Model, schemaName, tableName string, schema *ir.Schema) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", g.tableRef(schemaName, tableName))

	var cols []string
	for _, f := range model.ScalarFields() {
		cols = append(cols, "  "+g.columnDefForTable(model, f))
	}

	inlinePKColumn := g.sqliteInlineAutoIncrementPrimaryKeyColumn(model)
	if model.PrimaryKey != nil && len(model.PrimaryKey.Fields) > 0 && inlinePKColumn == "" {
		quoted := g.quoteIDs(normalizeFieldNames(model, model.PrimaryKey.Fields))
		cols = append(cols, fmt.Sprintf("  PRIMARY KEY (%s)", strings.Join(quoted, ", ")))
	}

	for _, uc := range modelUniqueConstraints(model) {
		fields := g.quoteIDs(normalizeFieldNames(model, uc.Fields))
		name := g.effectiveUniqueName(model.TableName(), uc.Name, normalizeFieldNames(model, uc.Fields))
		cols = append(cols, fmt.Sprintf("  CONSTRAINT %s UNIQUE (%s)", g.quoteID(name), strings.Join(fields, ", ")))
	}

	if g.Dialect == "sqlite" {
		for _, rel := range model.Relations {
			if len(rel.Fields) == 0 || len(rel.References) == 0 {
				continue
			}
			target := findModel(schema, rel.ToModel)
			refTable := rel.ToModel
			if target != nil {
				refTable = target.TableName()
			}
			localFields := g.quoteIDs(normalizeFieldNames(model, rel.Fields))
			refFields := g.quoteIDs(normalizeFieldNames(target, rel.References))
			fk := fmt.Sprintf("  FOREIGN KEY (%s) REFERENCES %s (%s)",
				strings.Join(localFields, ", "), g.quoteID(refTable), strings.Join(refFields, ", "))
			if onDelete := strings.TrimSpace(rel.OnDelete); onDelete != "" {
				fk += " ON DELETE " + onDelete
			}
			if onUpdate := strings.TrimSpace(rel.OnUpdate); onUpdate != "" {
				fk += " ON UPDATE " + onUpdate
			}
			cols = append(cols, fk)
		}
	}

	b.WriteString(strings.Join(cols, ",\n"))
	b.WriteString("\n);")
	return b.String()
}

// addColumnSQL never inlines UNIQUE: the planner emits an AddUnique change
// for a new unique column, and an inline clause would add a second constraint.
func (g DDLGenerator) addColumnSQL(c Change, cs *Changeset) string {
	f := findTableField(cs.New, c.Schema, c.Model, c.Field)
	if f == nil {
		return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;",
			g.tableRef(c.Schema, c.Model), g.quoteID(c.Field), g.sqlType(c.NewValue))
	}
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;",
		g.tableRef(c.Schema, c.Model), g.columnDefInternal(nil, f, false))
}

func (g DDLGenerator) alterTypeSQL(c Change) string {
	tbl := g.tableRef(c.Schema, c.Model)
	col := g.quoteID(c.Field)
	newType := g.sqlTypeSignature(c.NewValue)
	if c.NewField != nil {
		newType = g.columnSQLType(c.NewField)
	}

	switch g.Dialect {
	case "postgresql":
		return g.postgresAlterTypeSQL(tbl, col, newType, c)
	case "mysql":
		if c.NewField != nil {
			return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", tbl, g.columnDefForModify(c.NewField))
		}
		return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s;", tbl, col, newType)
	default: // sqlite
		return fmt.Sprintf("-- SQLite: ALTER COLUMN not supported; table rebuild required for %s.%s to %s", c.Model, c.Field, newType)
	}
}

// postgresAlterTypeSQL converts a column with an explicit USING cast. A
// column default may not cast to the new type, so the source default is
// dropped first and the target default set afterwards. Identity columns are
// left to AlterDefault (see postgresAlterTypeSetsDefault).
func (g DDLGenerator) postgresAlterTypeSQL(tbl, col, newType string, c Change) string {
	var statements []string
	if g.hasDatabaseDefault(c.OldField) {
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, col))
	}
	statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s;",
		tbl, col, newType, postgresUsingCast(col, newType, columnTypeOf(c.OldField, c.OldValue), columnTypeOf(c.NewField, c.NewValue))))
	if !g.isAutoIncrementField(c.OldField) && g.hasDatabaseDefault(c.NewField) {
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExprForField(c.NewField)))
	}
	return strings.Join(statements, "\n")
}

// postgresAlterTypeSetsDefault reports whether an AlterDefault change is
// applied by an AlterType change of the same column, which must set the
// default after converting the column in both migration directions. Identity
// transitions stay separate AlterDefault statements.
func postgresAlterTypeSetsDefault(cs *Changeset, c Change) bool {
	if cs == nil || isAutoIncrementDefault(c.OldValue) || isAutoIncrementDefault(c.NewValue) {
		return false
	}
	for _, other := range cs.Changes {
		if other.Type == AlterType && other.Schema == c.Schema && other.Model == c.Model && other.Field == c.Field {
			return true
		}
	}
	return false
}

// columnType classifies a column type for choosing a PostgreSQL USING cast.
type columnType struct {
	category string // text, numeric, integer, bool, enum, or the scalar name
	isList   bool
}

func columnTypeOf(f *ir.Field, signature string) columnType {
	if f != nil {
		t := columnType{isList: f.IsList}
		switch {
		case f.Type == ir.FieldKindEnum:
			t.category = "enum"
		case f.NativeType != nil:
			t.category = nativeTypeCategory(f.NativeType.Name, f.ScalarType)
		default:
			t.category = scalarTypeCategory(f.ScalarType)
		}
		return t
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return columnType{}
	}
	t := columnType{isList: strings.HasSuffix(signature, "[]")}
	signature = strings.TrimSuffix(signature, "[]")
	if base, native, ok := strings.Cut(signature, "@db."); ok {
		native, _, _ = strings.Cut(native, "(")
		t.category = nativeTypeCategory(native, base)
		return t
	}
	t.category = scalarTypeCategory(signature)
	return t
}

func scalarTypeCategory(scalar string) string {
	switch scalar {
	case "String":
		return "text"
	case "Int", "SmallInt", "BigInt":
		return "integer"
	case "Float", "Decimal":
		return "numeric"
	case "Boolean":
		return "bool"
	case "":
		return ""
	default:
		return scalar
	}
}

func nativeTypeCategory(native, scalar string) string {
	switch strings.ToLower(native) {
	case "text", "varchar", "char", "character", "bpchar", "citext", "name":
		return "text"
	case "int", "integer", "smallint", "bigint", "int2", "int4", "int8":
		return "integer"
	case "decimal", "numeric", "real", "doubleprecision", "double", "float4", "float8", "money":
		return "numeric"
	case "boolean", "bool":
		return "bool"
	}
	return scalarTypeCategory(scalar)
}

// postgresUsingCast returns the USING expression converting col to newType.
// Text converts to and from every type, numeric types convert among
// themselves, and integers convert to and from booleans. Other conversions,
// including every enum conversion, go through text, which always parses and
// converts values by their text representation. A scalar becomes a
// one-element array.
func postgresUsingCast(col, newType string, from, to columnType) string {
	if from.category != "" && !from.isList && to.isList && strings.HasSuffix(newType, "[]") {
		element := columnType{category: to.category}
		return "ARRAY[" + postgresUsingCast(col, strings.TrimSuffix(newType, "[]"), from, element) + "]"
	}
	direct := from.category != "enum" && to.category != "enum" && from.isList == to.isList
	if direct {
		numeric := func(c string) bool { return c == "integer" || c == "numeric" }
		switch {
		case from.category == "" || to.category == "":
		case from.category == to.category:
		case from.category == "text" || to.category == "text":
		case numeric(from.category) && numeric(to.category):
		case from.category == "bool" && to.category == "integer", from.category == "integer" && to.category == "bool":
		default:
			direct = false
		}
	}
	if direct {
		return col + "::" + newType
	}
	return col + "::text::" + newType
}

func (g DDLGenerator) alterNullSQL(c Change) string {
	tbl := g.tableRef(c.Schema, c.Model)
	col := g.quoteID(c.Field)

	nullable := c.NewValue == "optional"

	switch g.Dialect {
	case "postgresql":
		if nullable {
			return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", tbl, col)
		}
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", tbl, col)
	case "mysql":
		if c.NewField != nil {
			return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", tbl, g.columnDefForModify(c.NewField))
		}
		return fmt.Sprintf("-- MySQL: nullability change requires the full column definition for %s.%s", c.Model, c.Field)
	default: // sqlite
		return fmt.Sprintf("-- SQLite: nullability change not supported inline for %s.%s", c.Model, c.Field)
	}
}

func (g DDLGenerator) alterDefaultSQL(c Change) string {
	if g.Dialect == "postgresql" {
		return g.postgresAlterDefaultSQL(c)
	}
	tbl := g.tableRef(c.Schema, c.Model)
	col := g.quoteID(c.Field)

	if c.NewValue == "" {
		switch g.Dialect {
		case "mysql":
			return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, col)
		default:
			return fmt.Sprintf("-- SQLite: DROP DEFAULT not supported for %s.%s", c.Model, c.Field)
		}
	}

	if isAutoIncrementDefault(c.NewValue) {
		switch g.Dialect {
		case "mysql":
			return fmt.Sprintf("-- MySQL: adding AUTO_INCREMENT requires MODIFY COLUMN with the full column definition for %s.%s", c.Model, c.Field)
		default:
			return fmt.Sprintf("-- SQLite: adding AUTOINCREMENT requires table rebuild for %s.%s", c.Model, c.Field)
		}
	}

	switch g.Dialect {
	case "mysql":
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExprForChange(c))
	default:
		return fmt.Sprintf("-- SQLite: SET DEFAULT not supported inline for %s.%s", c.Model, c.Field)
	}
}

// postgresAlterDefaultSQL changes a column default. autoincrement() maps to
// an identity column, which has no DEFAULT expression: it is removed with
// DROP IDENTITY, and an added identity starts after the column's current
// maximum value.
func (g DDLGenerator) postgresAlterDefaultSQL(c Change) string {
	tbl := g.tableRef(c.Schema, c.Model)
	col := g.quoteID(c.Field)
	oldIdentity := isAutoIncrementDefault(c.OldValue)
	newIdentity := isAutoIncrementDefault(c.NewValue)

	var statements []string
	switch {
	case oldIdentity && newIdentity:
		return ""
	case oldIdentity:
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP IDENTITY IF EXISTS;", tbl, col))
		if c.NewValue != "" {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExprForChange(c)))
		} else {
			// serial columns (introspected as autoincrement) use a nextval()
			// default rather than identity; after DROP IDENTITY this is a
			// harmless no-op for identity columns.
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, col))
		}
	case newIdentity:
		if c.OldValue != "" {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, col))
		}
		statements = append(statements,
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s ADD GENERATED BY DEFAULT AS IDENTITY;", tbl, col),
			fmt.Sprintf("SELECT setval(pg_get_serial_sequence(%s, %s), GREATEST(COALESCE(MAX(%s), 0), 0) + 1, false) FROM %s;",
				quoteSQLString(tbl), quoteSQLString(c.Field), col, tbl))
	case c.NewValue == "":
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, col))
	default:
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExprForChange(c)))
	}
	return strings.Join(statements, "\n")
}

func (g DDLGenerator) addIndexSQL(c Change) string {
	name := c.NewValue
	if name == "" {
		name = DefaultIndexNameFor(g.Dialect, c.Model, splitDetailList(c.Details["fields"]), c.Details["unique"] == "true")
	}
	unique := ""
	if c.Details["unique"] == "true" {
		unique = "UNIQUE "
	}
	return g.createIndexSQL(name, c.Schema, c.Model, unique, g.indexColumnsFromDetails(c.Details), c.Details["where"], detailsHaveExpressions(c.Details))
}

// createIndexSQL creates an index on a table. PostgreSQL places the index in
// the table's schema, so the index name itself is never qualified.
func (g DDLGenerator) createIndexSQL(name, schemaName, tableName, unique, columns, where string, hasExpression bool) string {
	if hasExpression && g.Dialect != "postgresql" {
		return fmt.Sprintf("-- %s: PostgreSQL expression index %s is not supported", strings.ToUpper(g.Dialect), name)
	}
	if strings.TrimSpace(where) != "" && g.Dialect == "mysql" {
		return fmt.Sprintf("-- MySQL: partial indexes are not supported for index %s", name)
	}
	sql := fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)",
		unique, g.quoteID(name), g.tableRef(schemaName, tableName), columns)
	if strings.TrimSpace(where) != "" {
		sql += " WHERE " + strings.TrimSpace(where)
	}
	return sql + ";"
}

func (g DDLGenerator) indexColumnsSQL(model *ir.Model, idx *ir.Index) string {
	cols := effectiveIndexColumns(idx)
	parts := make([]string, 0, len(cols))
	for _, col := range cols {
		dbField := resolveColumnName(model, col.Field)
		parts = append(parts, g.indexColumnSQL(ir.IndexColumn{
			Field:      dbField,
			Expression: col.Expression,
			Sort:       col.Sort,
			Nulls:      col.Nulls,
			OpClass:    col.OpClass,
			Collation:  col.Collation,
		}))
	}
	return strings.Join(parts, ", ")
}

func (g DDLGenerator) indexColumnsFromDetails(details map[string]string) string {
	fields := splitDetailList(details["fields"])
	expressions := splitIndexExpressions(details["expressions"])
	sorts := splitDetailList(details["sorts"])
	nulls := splitDetailList(details["nulls"])
	opclasses := splitDetailList(details["opclasses"])
	collations := splitDetailList(details["collations"])
	count := maxInt(len(fields), len(expressions), len(sorts), len(nulls), len(opclasses), len(collations))
	if len(fields) < count {
		fields = append(fields, make([]string, count-len(fields))...)
	}
	parts := make([]string, 0, len(fields))
	for i, field := range fields {
		parts = append(parts, g.indexColumnSQL(ir.IndexColumn{
			Field:      field,
			Expression: detailAt(expressions, i),
			Sort:       detailAt(sorts, i),
			Nulls:      detailAt(nulls, i),
			OpClass:    detailAt(opclasses, i),
			Collation:  detailAt(collations, i),
		}))
	}
	return strings.Join(parts, ", ")
}

func (g DDLGenerator) indexColumnSQL(col ir.IndexColumn) string {
	sql := g.quoteID(col.Field)
	if strings.TrimSpace(col.Expression) != "" {
		sql = strings.TrimSpace(col.Expression)
	}
	if col.Collation != "" {
		sql += " COLLATE " + g.collationSQL(col.Collation)
	}
	if col.OpClass != "" && g.Dialect == "postgresql" {
		sql += " " + col.OpClass
	}
	if col.Sort != "" {
		sql += " " + strings.ToUpper(col.Sort)
	}
	if col.Nulls != "" && g.Dialect != "mysql" {
		sql += " NULLS " + strings.ToUpper(col.Nulls)
	}
	return sql
}

func (g DDLGenerator) collationSQL(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if g.Dialect != "postgresql" || strings.Contains(name, `"`) {
		return name
	}
	parts := strings.Split(name, ".")
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = g.quoteID(part)
	}
	return strings.Join(quoted, ".")
}

func splitDetailList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func splitIndexExpressions(s string) []string {
	if s == "" {
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(s), "[") {
		var expressions []string
		if json.Unmarshal([]byte(s), &expressions) == nil {
			return expressions
		}
	}
	return strings.Split(s, ",")
}

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func indexHasExpressions(idx *ir.Index) bool {
	for _, col := range effectiveIndexColumns(idx) {
		if strings.TrimSpace(col.Expression) != "" {
			return true
		}
	}
	return false
}

func detailsHaveExpressions(details map[string]string) bool {
	for _, expression := range splitIndexExpressions(details["expressions"]) {
		if strings.TrimSpace(expression) != "" {
			return true
		}
	}
	return false
}

func detailAt(values []string, idx int) string {
	if idx < len(values) {
		return values[idx]
	}
	return ""
}

// dropIndexSQL drops an index. For a generated name that was shortened by
// LimitIdentifier it also drops the full name, which PostgreSQL truncated
// when older versions created the index.
func (g DDLGenerator) dropIndexSQL(c Change) string {
	fields := splitDetailList(c.Details["fields"])
	unique := c.Details["unique"] == "true"
	name := c.OldValue
	if name == "" {
		name = DefaultIndexNameFor(g.Dialect, c.Model, fields, unique)
	}
	switch g.Dialect {
	case "mysql":
		return fmt.Sprintf("DROP INDEX %s ON %s;", g.quoteID(name), g.quoteID(c.Model))
	case "postgresql":
		sql := fmt.Sprintf("DROP INDEX IF EXISTS %s;", g.qualifiedName(c.Schema, name))
		prefix := "idx_"
		if unique {
			prefix = "uq_"
		}
		if legacy := prefix + c.Model + "_" + strings.Join(fields, "_"); legacy != name && LimitIdentifier(legacy) == name {
			sql += fmt.Sprintf("\nDROP INDEX IF EXISTS %s;", g.qualifiedName(c.Schema, legacy))
		}
		return sql
	default:
		return fmt.Sprintf("DROP INDEX IF EXISTS %s;", g.quoteID(name))
	}
}

func (g DDLGenerator) addUniqueSQL(c Change) string {
	fields := g.quoteIDs(strings.Split(c.Details["fields"], ","))
	name := c.NewValue
	name = g.effectiveUniqueName(c.Model, name, strings.Split(c.Details["fields"], ","))
	if g.Dialect == "postgresql" {
		return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s);",
			g.tableRef(c.Schema, c.Model), g.quoteID(name), strings.Join(fields, ", "))
	}
	return fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s);",
		g.quoteID(name), g.quoteID(c.Model), strings.Join(fields, ", "))
}

// dropUniqueSQL drops a unique constraint. An unnamed PostgreSQL constraint
// may carry the generated name, the full generated name truncated by older
// versions, or the name PostgreSQL assigned, so each is dropped if present.
func (g DDLGenerator) dropUniqueSQL(c Change) string {
	name := c.OldValue
	fields := strings.Split(c.Details["fields"], ",")
	switch g.Dialect {
	case "postgresql":
		tbl := g.tableRef(c.Schema, c.Model)
		if name == "" {
			names := []string{g.effectiveUniqueName(c.Model, "", fields)}
			if full := "uq_" + c.Model + "_" + strings.Join(fields, "_"); full != names[0] {
				names = append(names, full)
			}
			names = append(names, PostgresUniqueKeyName(c.Model, fields))
			statements := make([]string, len(names))
			for i, n := range names {
				statements[i] = fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", tbl, g.quoteID(n))
			}
			return strings.Join(statements, "\n")
		}
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", tbl, g.quoteID(name))
	case "mysql":
		name = g.effectiveUniqueName(c.Model, name, fields)
		return fmt.Sprintf("DROP INDEX %s ON %s;", g.quoteID(name), g.quoteID(c.Model))
	default:
		name = g.effectiveUniqueName(c.Model, name, fields)
		return fmt.Sprintf("DROP INDEX IF EXISTS %s;", g.quoteID(name))
	}
}

func (g DDLGenerator) effectiveUniqueName(table, name string, fields []string) string {
	if name != "" {
		return name
	}
	return DefaultUniqueNameFor(g.Dialect, table, fields)
}

func modelUniqueConstraints(model *ir.Model) []*ir.UniqueConstraint {
	if model == nil {
		return nil
	}
	out := make([]*ir.UniqueConstraint, 0, len(model.UniqueConstraints)+len(model.Fields))
	seen := make(map[string]bool)
	for _, uc := range model.UniqueConstraints {
		key := strings.Join(uc.Fields, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, uc)
	}
	for _, field := range model.ScalarFields() {
		if !field.IsUnique || field.IsID {
			continue
		}
		key := field.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, &ir.UniqueConstraint{Fields: []string{field.Name}})
	}
	return out
}

func (g DDLGenerator) changePKSQL(c Change) string {
	tbl := g.tableRef(c.Schema, c.Model)
	var parts []string

	if c.OldValue != "" {
		switch g.Dialect {
		case "mysql":
			parts = append(parts, fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY;", tbl))
		default:
			parts = append(parts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;",
				tbl, g.quoteID(PostgresPrimaryKeyName(c.Model))))
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
	if g.Dialect == "sqlite" {
		return fmt.Sprintf("-- SQLite: adding foreign key constraints requires table rebuild for %s", c.Model)
	}
	tbl := g.tableRef(c.Schema, c.Model)
	localFields := g.quoteIDs(strings.Split(c.Details["fields"], ","))
	refFields := g.quoteIDs(strings.Split(c.Details["references"], ","))
	refTable := g.tableRef(c.Details["toSchema"], c.Details["toModel"])
	name := c.Details["name"]
	if name == "" {
		name = DefaultForeignKeyNameFor(g.Dialect, c.Model, splitDetailList(c.Details["fields"]))
	}
	constraintName := g.quoteID(name)

	sql := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
		tbl, constraintName, strings.Join(localFields, ", "),
		refTable, strings.Join(refFields, ", "))
	if onDelete := strings.TrimSpace(c.Details["onDelete"]); onDelete != "" {
		sql += " ON DELETE " + onDelete
	}
	if onUpdate := strings.TrimSpace(c.Details["onUpdate"]); onUpdate != "" {
		sql += " ON UPDATE " + onUpdate
	}
	return sql + ";"
}

func (g DDLGenerator) orderedChanges(cs *Changeset) []Change {
	if cs == nil || len(cs.Changes) == 0 {
		return nil
	}

	dropFKs := make([]Change, 0)
	createEnums := make([]Change, 0)
	dropEnums := make([]Change, 0)
	others := make([]Change, 0)
	createTables := make([]Change, 0)
	addFKs := make([]Change, 0)
	dropTables := make([]Change, 0)
	var dropTriggers, addTriggers, schemaMoves []Change

	for _, c := range cs.Changes {
		switch c.Type {
		case SetTableSchema:
			schemaMoves = append(schemaMoves, c)
		case CreateEnum:
			createEnums = append(createEnums, c)
		case RenameEnum, AlterEnum:
			// PostgreSQL changes enum types before any table change, so their
			// reversal runs after table changes are reverted. MySQL rewrites the
			// enum columns, which must exist by then.
			if g.Dialect == "postgresql" {
				createEnums = append(createEnums, c)
			} else {
				others = append(others, c)
			}
		case DropEnum:
			dropEnums = append(dropEnums, c)
		case DropFK:
			dropFKs = append(dropFKs, c)
		case CreateTable:
			createTables = append(createTables, c)
		case AddFK:
			addFKs = append(addFKs, c)
		case DropTable:
			dropTables = append(dropTables, c)
		case DropTrigger:
			dropTriggers = append(dropTriggers, c)
		case AddTrigger:
			addTriggers = append(addTriggers, c)
		default:
			others = append(others, c)
		}
	}

	ordered := make([]Change, 0, len(cs.Changes))
	// Triggers are dropped from their source tables before tables move;
	// every later change addresses a moved table in its target schema.
	ordered = append(ordered, dropTriggers...)
	ordered = append(ordered, schemaMoves...)
	ordered = append(ordered, createEnums...)
	ordered = append(ordered, dropFKs...)
	ordered = append(ordered, orderOtherChanges(others)...)
	ordered = append(ordered, g.sortTableChanges(createTables, cs.New, false)...)
	ordered = append(ordered, addFKs...)
	ordered = append(ordered, g.sortTableChanges(dropTables, cs.Old, true)...)
	ordered = append(ordered, dropEnums...)
	ordered = append(ordered, addTriggers...)
	return ordered
}

func orderOtherChanges(changes []Change) []Change {
	ordered := append([]Change(nil), changes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return schemaChangePriority(ordered[i].Type) < schemaChangePriority(ordered[j].Type)
	})
	return ordered
}

func schemaChangePriority(changeType ChangeType) int {
	switch changeType {
	case DropIndex, DropUnique:
		return 0
	case AddIndex, AddUnique:
		return 2
	default:
		return 1
	}
}

func (g DDLGenerator) sortTableChanges(changes []Change, schema *ir.Schema, reverse bool) []Change {
	if len(changes) < 2 {
		return changes
	}

	changeByTable := make(map[string]Change, len(changes))
	tables := make([]string, 0, len(changes))
	for _, c := range changes {
		key := tableKey(c.Schema, c.Model)
		changeByTable[key] = c
		tables = append(tables, key)
	}

	orderedTables := topoSortTables(schema, tables)
	if reverse {
		for i, j := 0, len(orderedTables)-1; i < j; i, j = i+1, j-1 {
			orderedTables[i], orderedTables[j] = orderedTables[j], orderedTables[i]
		}
	}

	ordered := make([]Change, 0, len(changes))
	for _, tableName := range orderedTables {
		ordered = append(ordered, changeByTable[tableName])
	}
	return ordered
}

// topoSortTables orders table keys (see tableKey) so that referenced tables
// come before the tables whose foreign keys reference them.
func topoSortTables(schema *ir.Schema, tables []string) []string {
	if len(tables) == 0 {
		return nil
	}

	tableSet := make(map[string]struct{}, len(tables))
	for _, tableName := range tables {
		tableSet[tableName] = struct{}{}
	}

	dependents := make(map[string][]string, len(tables))
	indegree := make(map[string]int, len(tables))
	for _, tableName := range tables {
		indegree[tableName] = 0
	}

	if schema != nil {
		for _, model := range schema.Models {
			fromTable := modelKey(model)
			if _, ok := tableSet[fromTable]; !ok {
				continue
			}
			for _, rel := range model.Relations {
				if len(rel.Fields) == 0 || len(rel.References) == 0 {
					continue
				}
				targetModel := findModel(schema, rel.ToModel)
				if targetModel == nil {
					continue
				}
				toTable := modelKey(targetModel)
				if toTable == fromTable {
					continue
				}
				if _, ok := tableSet[toTable]; !ok {
					continue
				}
				dependents[toTable] = append(dependents[toTable], fromTable)
				indegree[fromTable]++
			}
		}
	}

	queue := make([]string, 0, len(tables))
	for tableName, degree := range indegree {
		if degree == 0 {
			queue = append(queue, tableName)
		}
	}
	sort.Strings(queue)

	ordered := make([]string, 0, len(tables))
	for len(queue) > 0 {
		tableName := queue[0]
		queue = queue[1:]
		ordered = append(ordered, tableName)

		nextTables := dependents[tableName]
		sort.Strings(nextTables)
		for _, dependent := range nextTables {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
				sort.Strings(queue)
			}
		}
	}

	if len(ordered) == len(tables) {
		return ordered
	}

	remaining := make([]string, 0, len(tables)-len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, tableName := range ordered {
		seen[tableName] = struct{}{}
	}
	for _, tableName := range tables {
		if _, ok := seen[tableName]; !ok {
			remaining = append(remaining, tableName)
		}
	}
	sort.Strings(remaining)
	return append(ordered, remaining...)
}

// dropFKSQL drops a foreign key. Without an explicit name, PostgreSQL and
// SQLite also drop the names older versions generated: comma-joined fields
// and full names longer than the identifier limit.
func (g DDLGenerator) dropFKSQL(c Change) string {
	tbl := g.tableRef(c.Schema, c.Model)
	fields := splitDetailList(c.Details["fields"])
	name := c.Details["name"]
	if name == "" {
		name = DefaultForeignKeyNameFor(g.Dialect, c.Model, fields)
	}

	switch g.Dialect {
	case "mysql":
		return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;", tbl, g.quoteID(name))
	default:
		names := []string{name}
		if c.Details["name"] == "" {
			for _, legacy := range []string{"fk_" + c.Model + "_" + strings.Join(fields, "_"), legacyForeignKeyName(c.Model, fields)} {
				if legacy != names[len(names)-1] && legacy != name {
					names = append(names, legacy)
				}
			}
		}
		statements := make([]string, len(names))
		for i, n := range names {
			statements[i] = fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", tbl, g.quoteID(n))
		}
		return strings.Join(statements, "\n")
	}
}

// ---------------------------------------------------------------------------
// Down SQL generation (reverse operations)
// ---------------------------------------------------------------------------

func (g DDLGenerator) changeToDown(c Change, cs *Changeset) string {
	if cs == nil {
		cs = &Changeset{}
	}
	if cs.Old != nil {
		g.Schema = cs.Old
	}
	switch c.Type {
	case AddTrigger:
		return g.dropTriggerSQL(c.Schema, c.Model, c.Field)
	case DropTrigger:
		if cs != nil && findTableModel(cs.Old, c.Schema, c.Model) != nil && findTableModel(cs.New, c.Schema, c.Model) == nil {
			return fmt.Sprintf("-- WARNING: restore table %s before recreating its trigger %s", g.quoteID(c.Model), g.quoteID(c.Field))
		}
		steps := g.createTriggerSteps(c.Schema, c.Model, c.Trigger)
		statements := make([]string, 0, len(steps))
		for _, step := range steps {
			statements = append(statements, g.scriptSQL(step))
		}
		return strings.Join(statements, "\n\n")
	case CreateTable:
		return fmt.Sprintf("DROP TABLE %s;", g.tableRef(c.Schema, c.Model))
	case DropTable:
		return fmt.Sprintf("-- WARNING: destructive — cannot recreate %s without original schema", c.Model)
	case SetTableSchema:
		if g.Dialect != "postgresql" {
			return ""
		}
		return g.scriptSQL(g.setTableSchemaStep(c.Model, c.NewValue, c.OldValue, cs.New, cs.Old))
	case AddColumn:
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", g.tableRef(c.Schema, c.Model), g.quoteID(c.Field))
	case DropColumn:
		return fmt.Sprintf("-- WARNING: destructive — cannot restore column %s.%s without original definition", c.Model, c.Field)
	case AlterType:
		rev := c
		rev.OldValue, rev.NewValue = c.NewValue, c.OldValue
		rev.OldField, rev.NewField = c.NewField, c.OldField
		return "-- REVIEW REQUIRED: reverting type change\n" + g.alterTypeSQL(rev)
	case AlterNull:
		rev := c
		rev.OldValue, rev.NewValue = c.NewValue, c.OldValue
		rev.OldField, rev.NewField = c.NewField, c.OldField
		return "-- REVIEW REQUIRED: reverting nullability change\n" + g.alterNullSQL(rev)
	case AlterDefault:
		if g.Dialect == "postgresql" && postgresAlterTypeSetsDefault(cs, c) {
			return ""
		}
		rev := c
		rev.OldValue, rev.NewValue = c.NewValue, c.OldValue
		rev.OldField, rev.NewField = c.NewField, c.OldField
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
		rev.Details = movedForeignKeyTarget(cs, c.Details)
		return g.addFKSQL(rev)
	case CreateEnum:
		if g.Dialect == "postgresql" {
			return "DROP TYPE IF EXISTS " + g.quoteID(enumDBName(cs.New, c.Model)) + ";"
		}
		return "" // MySQL and SQLite enums are inline column types; nothing to drop.
	case DropEnum:
		if g.Dialect == "postgresql" {
			if enum := findEnum(cs.Old, c.Model); enum != nil {
				return g.postgresCreateEnumSQL(enum)
			}
			return "-- WARNING: destructive — cannot recreate enum " + c.Model + " without original schema"
		}
		return "" // MySQL and SQLite enums are inline column types; nothing to recreate.
	case RenameEnum:
		if g.Dialect == "postgresql" {
			return g.postgresRenameEnumSQL(c.NewValue, c.OldValue)
		}
		return ""
	case AlterEnum:
		switch g.Dialect {
		case "postgresql":
			return g.postgresRevertEnumSQL(c, cs)
		case "mysql":
			if sql := g.mysqlRefreshEnumColumnsFrom(cs.Old, c.Model); sql != "" {
				return sql
			}
			return "-- REVIEW REQUIRED: cannot revert enum change for " + c.Model + " without original schema"
		}
		return "" // SQLite stores enum values as text.
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
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	default: // postgresql, sqlite
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// tableRef renders a table reference. On PostgreSQL a table with a schema
// (@@schema) is qualified; otherwise the name resolves through search_path.
func (g DDLGenerator) tableRef(schemaName, table string) string {
	return g.qualifiedName(schemaName, table)
}

// qualifiedName renders a schema-qualified object name on PostgreSQL. Other
// dialects have no schemas, so the schema is ignored.
func (g DDLGenerator) qualifiedName(schemaName, name string) string {
	if g.Dialect == "postgresql" && schemaName != "" {
		return g.quoteID(schemaName) + "." + g.quoteID(name)
	}
	return g.quoteID(name)
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
		"SmallInt": "SMALLINT",
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
		"SmallInt": "SMALLINT",
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
		"SmallInt": "INTEGER",
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

func (g DDLGenerator) sqlTypeSignature(typeName string) string {
	scalarType := strings.TrimSpace(typeName)
	isList := strings.HasSuffix(scalarType, "[]")
	if isList {
		scalarType = strings.TrimSuffix(scalarType, "[]")
	}
	base := g.sqlType(scalarType)
	if !isList {
		return base
	}
	switch g.Dialect {
	case "postgresql":
		return base + "[]"
	case "mysql":
		return "JSON"
	default:
		return "TEXT"
	}
}

// ---------------------------------------------------------------------------
// Column definition builder
// ---------------------------------------------------------------------------

// columnDefForModify renders a column definition for ALTER TABLE ... MODIFY
// COLUMN. Inline UNIQUE is omitted: each MODIFY would otherwise create a new
// duplicate unique index on MySQL.
func (g DDLGenerator) columnDefForModify(f *ir.Field) string {
	return g.columnDefInternal(nil, f, false)
}

func (g DDLGenerator) columnDefForTable(model *ir.Model, f *ir.Field) string {
	return g.columnDefInternal(model, f, true)
}

func (g DDLGenerator) columnDefInternal(model *ir.Model, f *ir.Field, allowInlineUnique bool) string {
	name := columnName(f)
	if g.sqliteInlineAutoIncrementPrimaryKeyColumn(model) == name {
		return strings.Join([]string{g.quoteID(name), "INTEGER", "PRIMARY KEY", "AUTOINCREMENT"}, " ")
	}

	var parts []string
	parts = append(parts, g.quoteID(name))
	parts = append(parts, g.columnSQLType(f))

	if g.isAutoIncrementField(f) {
		parts = append(parts, g.autoIncrementClause())
	}

	if !f.IsOptional {
		parts = append(parts, "NOT NULL")
	}

	if allowInlineUnique && f.IsUnique && !f.IsID && model == nil {
		parts = append(parts, "UNIQUE")
	}

	if expr, ok := generatedColumnExpr(f); ok {
		parts = append(parts, "GENERATED ALWAYS AS ("+expr+") STORED")
	} else if g.hasDatabaseDefault(f) {
		parts = append(parts, "DEFAULT "+g.defaultExprForField(f))
	}

	return strings.Join(parts, " ")
}

// hasDatabaseDefault reports whether the column carries a DEFAULT clause.
// Identity columns, client-generated cuid() values, and generated columns
// have none.
func (g DDLGenerator) hasDatabaseDefault(f *ir.Field) bool {
	return f != nil && f.Default != nil && !f.IsGenerated && !g.isAutoIncrementField(f) && !isCUIDDefault(f.Default)
}

// generatedColumnExpr returns the expression of a generated column.
func generatedColumnExpr(f *ir.Field) (string, bool) {
	if f == nil || !f.IsGenerated || f.Default == nil || !f.Default.IsFunction || f.Default.FuncName != "dbgenerated" || len(f.Default.FuncArgs) == 0 {
		return "", false
	}
	return dbGeneratedExpr(f.Default.FuncArgs[0]), true
}

func (g DDLGenerator) sqliteInlineAutoIncrementPrimaryKeyColumn(model *ir.Model) string {
	if g.Dialect != "sqlite" || model == nil || model.PrimaryKey == nil || len(model.PrimaryKey.Fields) != 1 {
		return ""
	}

	pkColumn := normalizeFieldNames(model, model.PrimaryKey.Fields)[0]
	for _, f := range model.ScalarFields() {
		if columnName(f) == pkColumn && g.isAutoIncrementField(f) {
			return pkColumn
		}
	}
	return ""
}

func (g DDLGenerator) isAutoIncrementField(f *ir.Field) bool {
	return f != nil && f.Default != nil && f.Default.IsFunction && f.Default.FuncName == "autoincrement"
}

func isAutoIncrementDefault(value string) bool {
	funcName, _, ok := parseDefaultFunction(value)
	return ok && funcName == "autoincrement"
}

func (g DDLGenerator) autoIncrementClause() string {
	switch g.Dialect {
	case "postgresql":
		return "GENERATED BY DEFAULT AS IDENTITY"
	case "mysql":
		return "AUTO_INCREMENT"
	case "sqlite":
		return "AUTOINCREMENT"
	default:
		return ""
	}
}

func (g DDLGenerator) columnSQLType(f *ir.Field) string {
	if f == nil {
		return g.sqlType("")
	}
	if f.NativeType != nil {
		if f.IsList && g.Dialect == "postgresql" {
			return g.nativeTypeSQL(f.NativeType) + "[]"
		}
		return g.nativeTypeSQL(f.NativeType)
	}
	if f.Type == ir.FieldKindEnum {
		if g.Dialect == "postgresql" {
			if f.IsList {
				return g.quoteID(enumDBName(g.Schema, f.EnumType)) + "[]"
			}
			return g.quoteID(enumDBName(g.Schema, f.EnumType))
		}
		if g.Dialect == "mysql" {
			if enum := findEnum(g.Schema, f.EnumType); enum != nil {
				values := make([]string, len(enum.Values))
				for i, value := range enum.Values {
					values[i] = quoteSQLString(enumValueLabel(value))
				}
				return "ENUM(" + strings.Join(values, ", ") + ")"
			}
		}
		return "TEXT"
	}
	base := f.ScalarType
	if f.IsList {
		base += "[]"
	}
	return g.sqlTypeSignature(base)
}

func (g DDLGenerator) nativeTypeSQL(nt *ir.NativeType) string {
	if nt == nil {
		return "TEXT"
	}
	name := nt.Name
	// Native type names originate in the validated @db namespace. Keep only
	// identifier characters here so they can never become an SQL fragment.
	for _, r := range name {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return "TEXT"
		}
	}
	if g.Dialect == "mysql" && len(nt.Args) == 0 {
		// MySQL spells unsigned integers as a modifier, not a type name.
		switch name {
		case "UnsignedInt":
			return "INT UNSIGNED"
		case "UnsignedBigInt":
			return "BIGINT UNSIGNED"
		}
	}
	if len(nt.Args) == 0 {
		return strings.ToUpper(name)
	}
	args := make([]string, len(nt.Args))
	for i, arg := range nt.Args {
		arg = strings.TrimSpace(arg)
		for _, r := range arg {
			if !(r >= '0' && r <= '9' || r == ',') {
				return "TEXT"
			}
		}
		args[i] = arg
	}
	return strings.ToUpper(name) + "(" + strings.Join(args, ",") + ")"
}

func findEnum(schema *ir.Schema, name string) *ir.Enum {
	if schema == nil {
		return nil
	}
	for _, enum := range schema.Enums {
		if enum.Name == name {
			return enum
		}
	}
	return nil
}

// enumValueDBLabel maps an enum value name to its database label (@map). A
// value that is not declared is returned unchanged.
func enumValueDBLabel(schema *ir.Schema, enumName, value string) string {
	if enum := findEnum(schema, enumName); enum != nil {
		for _, v := range enum.Values {
			if v.Name == value {
				return enumValueLabel(v)
			}
		}
	}
	return value
}

func (g DDLGenerator) defaultExprForField(f *ir.Field) string {
	if f == nil || f.Default == nil {
		return "''"
	}
	d := f.Default
	switch {
	case d.IsArray:
		return g.fieldArrayDefaultExpr(f)
	case d.IsFunction:
		return g.functionDefaultExpr(d.FuncName, d.FuncArgs)
	case f.Type == ir.FieldKindEnum:
		return quoteSQLString(enumValueDBLabel(g.Schema, f.EnumType, d.Value))
	case d.IsString:
		return quoteSQLString(d.Value)
	}
	return g.defaultExpr(formatDefault(d))
}

func quoteSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (g DDLGenerator) defaultExprForChange(c Change) string {
	if c.NewField != nil && c.NewField.Default != nil {
		return g.defaultExprForField(c.NewField)
	}
	if f := findTableField(g.Schema, c.Schema, c.Model, c.Field); f != nil && f.Default != nil {
		return g.defaultExprForField(f)
	}
	return g.defaultExpr(c.NewValue)
}

// fieldArrayDefaultExpr renders a list default with elements typed by the
// field. PostgreSQL ARRAY[...] and '{}' literals do not assign to enum,
// uuid, or timestamp arrays, so they are cast to the column type.
func (g DDLGenerator) fieldArrayDefaultExpr(f *ir.Field) string {
	values := make([]string, len(f.Default.ArrayValue))
	for i, value := range f.Default.ArrayValue {
		if f.Type == ir.FieldKindEnum {
			value = enumValueDBLabel(g.Schema, f.EnumType, value)
		}
		values[i] = value
	}
	if g.Dialect != "postgresql" {
		return g.arrayDefaultExpr(&ir.DefaultValue{IsArray: true, ArrayValue: values})
	}
	castType := g.columnSQLType(f)
	if !strings.HasSuffix(castType, "[]") {
		castType += "[]"
	}
	if len(values) == 0 {
		return "'{}'::" + castType
	}
	quoted := f.Type == ir.FieldKindEnum
	switch f.ScalarType {
	case "String", "UUID", "DateTime", "Json", "Bytes":
		quoted = true
	}
	parts := make([]string, len(values))
	for i, value := range values {
		if quoted {
			parts[i] = quoteSQLString(value)
		} else {
			parts[i] = g.defaultExpr(value)
		}
	}
	return "ARRAY[" + strings.Join(parts, ", ") + "]::" + castType
}

func (g DDLGenerator) arrayDefaultExpr(d *ir.DefaultValue) string {
	values := d.ArrayValue
	switch g.Dialect {
	case "postgresql":
		if len(values) == 0 {
			return "'{}'"
		}
		parts := make([]string, len(values))
		for i, value := range values {
			parts[i] = g.defaultExpr(value)
		}
		return "ARRAY[" + strings.Join(parts, ", ") + "]"
	case "mysql":
		if len(values) == 0 {
			return "(JSON_ARRAY())"
		}
		parts := make([]string, len(values))
		for i, value := range values {
			parts[i] = g.defaultExpr(value)
		}
		return "(JSON_ARRAY(" + strings.Join(parts, ", ") + "))"
	default:
		if len(values) == 0 {
			return "'[]'"
		}
		return quoteSQLString("[" + strings.Join(values, ",") + "]")
	}
}

// defaultExpr wraps a default value expression for SQL.
func (g DDLGenerator) defaultExpr(val string) string {
	if val == "" {
		return "''"
	}
	if funcName, args, ok := parseDefaultFunction(val); ok {
		return g.functionDefaultExpr(funcName, args)
	}
	// Function-style defaults without a dialect-specific rewrite pass through.
	if isFunctionDefault(val) {
		return val
	}
	// Bare booleans and numbers pass through.
	if val == "true" || val == "false" {
		return val
	}
	for _, ch := range val {
		if (ch < '0' || ch > '9') && ch != '.' && ch != '-' {
			return quoteSQLString(val)
		}
	}
	return val
}

// functionDefaultExpr renders a default function call. dbgenerated() emits
// its expression verbatim; other functions map through the dialect.
func (g DDLGenerator) functionDefaultExpr(funcName string, args []string) string {
	if funcName == "dbgenerated" {
		if len(args) == 0 {
			return "NULL"
		}
		return dbGeneratedExpr(args[0])
	}
	if d := g.sqlDialect(); d != nil {
		return d.DefaultValueExpression(funcName, args)
	}
	return formatDefault(&ir.DefaultValue{IsFunction: true, FuncName: funcName, FuncArgs: args})
}

// dbGeneratedExpr returns the SQL expression of a dbgenerated() argument. The
// schema parser already unquotes string arguments; a double-quoted string
// literal that reaches this point is unquoted as well.
func dbGeneratedExpr(arg string) string {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, `"`) {
		if unquoted, err := strconv.Unquote(arg); err == nil {
			return strings.TrimSpace(unquoted)
		}
	}
	return arg
}

func (g DDLGenerator) sqlDialect() runtimepkg.Dialect {
	switch g.Dialect {
	case "postgresql":
		return runtimedialect.PostgreSQL{}
	case "mysql":
		return runtimedialect.MySQL{}
	case "sqlite":
		return runtimedialect.SQLite{}
	default:
		return nil
	}
}

// parseDefaultFunction parses a formatted function default such as
// uuid() or dbgenerated(expr). dbgenerated takes a single SQL expression, so
// its argument is never split; other arguments split on top-level commas.
func parseDefaultFunction(val string) (string, []string, bool) {
	trimmed := strings.TrimSpace(val)
	if !isFunctionDefault(trimmed) {
		return "", nil, false
	}

	openIdx := strings.IndexByte(trimmed, '(')
	funcName := strings.TrimSpace(trimmed[:openIdx])
	argsText := strings.TrimSpace(trimmed[openIdx+1 : len(trimmed)-1])
	if funcName == "" {
		return "", nil, false
	}
	for _, r := range funcName {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return "", nil, false
		}
	}
	if argsText == "" {
		return funcName, nil, true
	}
	if funcName == "dbgenerated" {
		return funcName, []string{argsText}, true
	}
	return funcName, splitTopLevelArgs(argsText), true
}

// splitTopLevelArgs splits on commas outside parentheses and quotes.
func splitTopLevelArgs(text string) []string {
	var args []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '\'' || ch == '"':
			quote = ch
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		case ch == ',' && depth == 0:
			args = append(args, strings.TrimSpace(text[start:i]))
			start = i + 1
		}
	}
	return append(args, strings.TrimSpace(text[start:]))
}

func isFunctionDefault(val string) bool {
	trimmed := strings.TrimSpace(val)
	openIdx := strings.IndexByte(trimmed, '(')
	return openIdx > 0 && strings.HasSuffix(trimmed, ")")
}
