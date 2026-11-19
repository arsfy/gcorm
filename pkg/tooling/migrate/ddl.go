// ddl.go generates dialect-specific DDL SQL from a migration changeset.
package migrate

import (
	"encoding/json"
	"fmt"
	"sort"
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
	script := make([]string, 0, len(steps))
	for _, step := range steps {
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
type DDLStep struct {
	SQL             string
	SingleStatement bool
}

// UpSteps returns the SQL for database drivers applying an up migration.
// Callers applying a SQLite rebuild must manage its transaction and PRAGMAs.
func (g DDLGenerator) UpSteps(cs *Changeset) []DDLStep {
	if cs == nil {
		return nil
	}
	var steps []DDLStep
	if g.Dialect == "postgresql" {
		if sql := g.postgresEnumChanges(cs); sql != "" {
			steps = append(steps, DDLStep{SQL: sql})
		}
	}
	createdTables := make(map[string]bool)
	for _, c := range cs.Changes {
		if c.Type == CreateTable {
			createdTables[c.Model] = true
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
			steps = append(steps, g.createTriggerSteps(c.Model, c.Trigger)...)
			continue
		case DropTrigger:
			steps = append(steps, DDLStep{SQL: g.dropTriggerSQL(c.Model, c.Field)})
			continue
		}
		if rebuildTables[c.Model] {
			if !rebuilt[c.Model] {
				steps = append(steps, DDLStep{SQL: g.sqliteRebuildTableSQL(c.Model, cs)})
				rebuilt[c.Model] = true
			}
			continue
		}
		if g.Dialect == "sqlite" && c.Type == AddFK && createdTables[c.Model] {
			continue
		}
		if sql := g.changeToUp(c, cs); sql != "" {
			steps = append(steps, DDLStep{SQL: sql})
		}
	}
	return append(steps, restore...)
}

func (g DDLGenerator) postgresEnumChanges(cs *Changeset) string {
	oldEnums := map[string]*ir.Enum{}
	if cs.Old != nil {
		for _, e := range cs.Old.Enums {
			oldEnums[e.Name] = e
		}
	}
	var statements []string
	if cs.New == nil {
		return ""
	}
	for _, e := range cs.New.Enums {
		name := enumDBName(cs.New, e.Name)
		old := oldEnums[e.Name]
		if old == nil {
			statements = append(statements, g.postgresCreateEnumSQL(e))
			continue
		}
		seen := map[string]bool{}
		for _, v := range old.Values {
			seen[v.Name] = true
		}
		for _, v := range e.Values {
			if !seen[v.Name] {
				value := v.Name
				if v.DBName != "" {
					value = v.DBName
				}
				statements = append(statements, "ALTER TYPE "+g.quoteID(name)+" ADD VALUE IF NOT EXISTS "+quoteSQLString(value)+";")
			}
		}
	}
	return strings.Join(statements, "\n")
}

// enumDBName returns the database name of an enum type, honoring @@map.
// It falls back to the given name when the enum is not in the schema.
func enumDBName(schema *ir.Schema, name string) string {
	if e := findEnum(schema, name); e != nil && e.DBName != "" {
		return e.DBName
	}
	return name
}

// postgresCreateEnumSQL renders a guarded CREATE TYPE for an enum.
// PostgreSQL has no CREATE TYPE IF NOT EXISTS, so a DO block swallows
// duplicate_object errors to tolerate schema drift.
func (g DDLGenerator) postgresCreateEnumSQL(e *ir.Enum) string {
	values := make([]string, len(e.Values))
	for i, v := range e.Values {
		value := v.Name
		if v.DBName != "" {
			value = v.DBName
		}
		values[i] = quoteSQLString(value)
	}
	name := e.Name
	if e.DBName != "" {
		name = e.DBName
	}
	stmt := "CREATE TYPE " + g.quoteID(name) + " AS ENUM (" + strings.Join(values, ", ") + ");"
	delimiter := "$$"
	for strings.Contains(stmt, delimiter) {
		delimiter = "$gco" + delimiter[1:]
	}
	return "DO " + delimiter + " BEGIN\n" + stmt + "\nEXCEPTION WHEN duplicate_object THEN NULL;\nEND " + delimiter + ";"
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
	create := g.createTableDefinitionSQL(newModel, temp, cs.New)
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
		if oldCols[col] {
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
			cols = append(cols, columnName(f))
			value := "NULL"
			if f.Default != nil && !g.isAutoIncrementField(f) {
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
		name := effectiveIndexName(newModel, idx)
		unique := ""
		if idx.IsUnique {
			unique = "UNIQUE "
		}
		b.WriteString("\n")
		b.WriteString(g.createIndexSQL(name, table, unique, g.indexColumnsSQL(newModel, idx), idx.Where, indexHasExpressions(idx)))
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
	case CreateEnum:
		return "" // PostgreSQL enum creation is emitted before table changes.
	case AlterEnum:
		if g.Dialect == "mysql" {
			return g.mysqlRefreshEnumColumns(c.Model)
		}
		return "" // PostgreSQL additions are emitted in postgresEnumChanges; SQLite stores enum values as text.
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
			values[i] = quoteSQLString(value.Name)
		}
		enumType = "ENUM(" + strings.Join(values, ", ") + ")"
	}
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
			if f.Default != nil {
				parts = append(parts, "DEFAULT "+g.defaultExprForField(f))
			}
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", g.quoteID(m.TableName()), strings.Join(parts, " ")))
		}
	}
	return strings.Join(statements, "\n")
}

func (g DDLGenerator) createTableSQL(c Change, cs *Changeset) string {
	model := findModel(cs.New, c.Model)
	if model == nil {
		return fmt.Sprintf("-- CreateTable %s: model not found in target schema", c.Model)
	}

	var b strings.Builder
	b.WriteString(g.createTableDefinitionSQL(model, model.TableName(), cs.New))
	for _, idx := range model.Indexes {
		name := effectiveIndexName(model, idx)
		unique := ""
		if idx.IsUnique {
			unique = "UNIQUE "
		}
		b.WriteString("\n")
		b.WriteString(g.createIndexSQL(name, model.TableName(), unique, g.indexColumnsSQL(model, idx), idx.Where, indexHasExpressions(idx)))
	}
	return b.String()
}

func (g DDLGenerator) createTableDefinitionSQL(model *ir.Model, tableName string, schema *ir.Schema) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", g.quoteID(tableName))

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
		name := effectiveUniqueName(model.TableName(), uc.Name, normalizeFieldNames(model, uc.Fields))
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
	newType := g.sqlTypeSignature(c.NewValue)
	if c.NewField != nil {
		newType = g.columnSQLType(c.NewField)
	}

	switch g.Dialect {
	case "postgresql":
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", tbl, col, newType)
	case "mysql":
		if c.NewField != nil {
			return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", tbl, g.columnDefForModify(c.NewField))
		}
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
		if c.NewField != nil {
			return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", tbl, g.columnDefForModify(c.NewField))
		}
		return fmt.Sprintf("-- MySQL: nullability change requires the full column definition for %s.%s", c.Model, c.Field)
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

	if isAutoIncrementDefault(c.NewValue) {
		switch g.Dialect {
		case "postgresql":
			return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s ADD GENERATED BY DEFAULT AS IDENTITY;", tbl, col)
		case "mysql":
			return fmt.Sprintf("-- MySQL: adding AUTO_INCREMENT requires MODIFY COLUMN with the full column definition for %s.%s", c.Model, c.Field)
		default:
			return fmt.Sprintf("-- SQLite: adding AUTOINCREMENT requires table rebuild for %s.%s", c.Model, c.Field)
		}
	}

	switch g.Dialect {
	case "postgresql":
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExprForChange(c))
	case "mysql":
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExprForChange(c))
	default:
		return fmt.Sprintf("-- SQLite: SET DEFAULT not supported inline for %s.%s", c.Model, c.Field)
	}
}

func (g DDLGenerator) addIndexSQL(c Change) string {
	name := c.NewValue
	if name == "" {
		name = fmt.Sprintf("idx_%s_%s", c.Model, c.Details["fields"])
	}
	unique := ""
	if c.Details["unique"] == "true" {
		unique = "UNIQUE "
	}
	return g.createIndexSQL(name, c.Model, unique, g.indexColumnsFromDetails(c.Details), c.Details["where"], detailsHaveExpressions(c.Details))
}

func (g DDLGenerator) createIndexSQL(name, tableName, unique, columns, where string, hasExpression bool) string {
	if hasExpression && g.Dialect != "postgresql" {
		return fmt.Sprintf("-- %s: PostgreSQL expression index %s is not supported", strings.ToUpper(g.Dialect), name)
	}
	if strings.TrimSpace(where) != "" && g.Dialect == "mysql" {
		return fmt.Sprintf("-- MySQL: partial indexes are not supported for index %s", name)
	}
	sql := fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)",
		unique, g.quoteID(name), g.quoteID(tableName), columns)
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

func (g DDLGenerator) dropIndexSQL(c Change) string {
	name := c.OldValue
	if name == "" {
		name = fmt.Sprintf("idx_%s_%s", c.Model, c.Details["fields"])
	}
	switch g.Dialect {
	case "mysql":
		return fmt.Sprintf("DROP INDEX %s ON %s;", g.quoteID(name), g.quoteID(c.Model))
	default:
		return fmt.Sprintf("DROP INDEX IF EXISTS %s;", g.quoteID(name))
	}
}

func (g DDLGenerator) addUniqueSQL(c Change) string {
	fields := g.quoteIDs(strings.Split(c.Details["fields"], ","))
	name := c.NewValue
	name = effectiveUniqueName(c.Model, name, strings.Split(c.Details["fields"], ","))
	if g.Dialect == "postgresql" {
		return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s);",
			g.quoteID(c.Model), g.quoteID(name), strings.Join(fields, ", "))
	}
	return fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s);",
		g.quoteID(name), g.quoteID(c.Model), strings.Join(fields, ", "))
}

func (g DDLGenerator) dropUniqueSQL(c Change) string {
	name := c.OldValue
	fields := strings.Split(c.Details["fields"], ",")
	switch g.Dialect {
	case "postgresql":
		if name == "" {
			canonical := effectiveUniqueName(c.Model, "", fields)
			legacy := postgresDefaultUniqueName(c.Model, fields)
			return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;\nALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;",
				g.quoteID(c.Model), g.quoteID(canonical), g.quoteID(c.Model), g.quoteID(legacy))
		}
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", g.quoteID(c.Model), g.quoteID(name))
	case "mysql":
		name = effectiveUniqueName(c.Model, name, fields)
		return fmt.Sprintf("DROP INDEX %s ON %s;", g.quoteID(name), g.quoteID(c.Model))
	default:
		name = effectiveUniqueName(c.Model, name, fields)
		return fmt.Sprintf("DROP INDEX IF EXISTS %s;", g.quoteID(name))
	}
}

func effectiveUniqueName(table, name string, fields []string) string {
	if name != "" {
		return name
	}
	return "uq_" + table + "_" + strings.Join(fields, "_")
}

func postgresDefaultUniqueName(table string, fields []string) string {
	return table + "_" + strings.Join(fields, "_") + "_key"
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
	tbl := g.quoteID(c.Model)
	var parts []string

	if c.OldValue != "" {
		switch g.Dialect {
		case "mysql":
			parts = append(parts, fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY;", tbl))
		default:
			parts = append(parts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;",
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
	if g.Dialect == "sqlite" {
		return fmt.Sprintf("-- SQLite: adding foreign key constraints requires table rebuild for %s", c.Model)
	}
	tbl := g.quoteID(c.Model)
	localFields := g.quoteIDs(strings.Split(c.Details["fields"], ","))
	refFields := g.quoteIDs(strings.Split(c.Details["references"], ","))
	refTable := g.quoteID(c.Details["toModel"])
	name := c.Details["name"]
	if name == "" {
		name = fmt.Sprintf("fk_%s_%s", c.Model, c.Details["fields"])
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
	var dropTriggers, addTriggers []Change

	for _, c := range cs.Changes {
		switch c.Type {
		case CreateEnum:
			createEnums = append(createEnums, c)
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
	ordered = append(ordered, dropTriggers...)
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
		changeByTable[c.Model] = c
		tables = append(tables, c.Model)
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
			fromTable := model.TableName()
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
				toTable := targetModel.TableName()
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

func (g DDLGenerator) dropFKSQL(c Change) string {
	tbl := g.quoteID(c.Model)
	name := c.Details["name"]
	if name == "" {
		name = fmt.Sprintf("fk_%s_%s", c.Model, c.Details["fields"])
	}
	constraintName := g.quoteID(name)

	switch g.Dialect {
	case "mysql":
		return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;", tbl, constraintName)
	default:
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", tbl, constraintName)
	}
}

// ---------------------------------------------------------------------------
// Down SQL generation (reverse operations)
// ---------------------------------------------------------------------------

func (g DDLGenerator) changeToDown(c Change, cs *Changeset) string {
	if cs != nil && cs.Old != nil {
		g.Schema = cs.Old
	}
	switch c.Type {
	case AddTrigger:
		return g.dropTriggerSQL(c.Model, c.Field)
	case DropTrigger:
		if cs != nil && findModel(cs.Old, c.Model) != nil && findModel(cs.New, c.Model) == nil {
			return fmt.Sprintf("-- WARNING: restore table %s before recreating its trigger %s", g.quoteID(c.Model), g.quoteID(c.Field))
		}
		steps := g.createTriggerSteps(c.Model, c.Trigger)
		statements := make([]string, 0, len(steps))
		for _, step := range steps {
			statements = append(statements, g.scriptSQL(step))
		}
		return strings.Join(statements, "\n\n")
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
		rev.OldValue, rev.NewValue = c.NewValue, c.OldValue
		rev.OldField, rev.NewField = c.NewField, c.OldField
		return "-- REVIEW REQUIRED: reverting type change\n" + g.alterTypeSQL(rev)
	case AlterNull:
		rev := c
		rev.OldValue, rev.NewValue = c.NewValue, c.OldValue
		rev.OldField, rev.NewField = c.NewField, c.OldField
		return "-- REVIEW REQUIRED: reverting nullability change\n" + g.alterNullSQL(rev)
	case AlterDefault:
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
	case AlterEnum:
		switch g.Dialect {
		case "postgresql":
			// PostgreSQL enum values cannot be removed; a true revert requires
			// recreating the type and rewriting dependent columns.
			return "-- REVIEW REQUIRED: cannot revert enum change for " + c.Model + "; PostgreSQL enum values cannot be removed"
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

func (g DDLGenerator) columnDef(f *ir.Field) string {
	return g.columnDefInternal(nil, f, true)
}

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

	if f.Default != nil && !g.isAutoIncrementField(f) {
		parts = append(parts, "DEFAULT "+g.defaultExprForField(f))
	}

	return strings.Join(parts, " ")
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
		return g.nativeTypeSQL(f.NativeType)
	}
	if f.Type == ir.FieldKindEnum {
		if g.Dialect == "postgresql" {
			return g.quoteID(enumDBName(g.Schema, f.EnumType))
		}
		if g.Dialect == "mysql" {
			if enum := findEnum(g.Schema, f.EnumType); enum != nil {
				values := make([]string, len(enum.Values))
				for i, value := range enum.Values {
					values[i] = quoteSQLString(value.Name)
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

func (g DDLGenerator) defaultExprForField(f *ir.Field) string {
	if f == nil || f.Default == nil {
		return "''"
	}
	if f.Default.IsArray {
		return g.arrayDefaultExpr(f.Default)
	}
	if f.Default.IsString {
		return quoteSQLString(f.Default.Value)
	}
	return g.defaultExpr(formatDefault(f.Default))
}

func quoteSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (g DDLGenerator) defaultExprForChange(c Change) string {
	if c.NewField != nil && c.NewField.Default != nil {
		return g.defaultExprForField(c.NewField)
	}
	if f := findField(g.Schema, c.Model, c.Field); f != nil && f.Default != nil {
		return g.defaultExprForField(f)
	}
	return g.defaultExpr(c.NewValue)
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
	if expr, ok := g.mappedDefaultFunction(val); ok {
		return expr
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

func (g DDLGenerator) mappedDefaultFunction(val string) (string, bool) {
	funcName, args, ok := parseDefaultFunction(val)
	if !ok {
		return "", false
	}
	if d := g.sqlDialect(); d != nil {
		return d.DefaultValueExpression(funcName, args), true
	}
	return val, true
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

	rawArgs := strings.Split(argsText, ",")
	args := make([]string, 0, len(rawArgs))
	for _, arg := range rawArgs {
		args = append(args, strings.TrimSpace(arg))
	}
	return funcName, args, true
}

func isFunctionDefault(val string) bool {
	trimmed := strings.TrimSpace(val)
	openIdx := strings.IndexByte(trimmed, '(')
	return openIdx > 0 && strings.HasSuffix(trimmed, ")")
}
