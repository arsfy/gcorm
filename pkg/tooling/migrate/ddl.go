// ddl.go generates dialect-specific DDL SQL from a migration changeset.
package migrate

import (
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
	var b strings.Builder
	createdTables := make(map[string]bool)
	if cs != nil {
		for _, c := range cs.Changes {
			if c.Type == CreateTable {
				createdTables[c.Model] = true
			}
		}
	}
	for _, c := range g.orderedChanges(cs) {
		if g.Dialect == "sqlite" && c.Type == AddFK && createdTables[c.Model] {
			continue
		}
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
		quoted := g.quoteIDs(normalizeFieldNames(model, model.PrimaryKey.Fields))
		cols = append(cols, fmt.Sprintf("  PRIMARY KEY (%s)", strings.Join(quoted, ", ")))
	}

	for _, uc := range model.UniqueConstraints {
		fields := g.quoteIDs(normalizeFieldNames(model, uc.Fields))
		if uc.Name != "" {
			cols = append(cols, fmt.Sprintf("  CONSTRAINT %s UNIQUE (%s)", g.quoteID(uc.Name), strings.Join(fields, ", ")))
			continue
		}
		cols = append(cols, fmt.Sprintf("  UNIQUE (%s)", strings.Join(fields, ", ")))
	}

	if g.Dialect == "sqlite" {
		for _, rel := range model.Relations {
			if len(rel.Fields) == 0 || len(rel.References) == 0 {
				continue
			}
			target := findModel(cs.New, rel.ToModel)
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

	for _, idx := range model.Indexes {
		name := idx.Name
		if name == "" {
			name = fmt.Sprintf("idx_%s_%s", model.TableName(), strings.Join(normalizeFieldNames(model, idx.Fields), "_"))
		}
		unique := ""
		if idx.IsUnique {
			unique = "UNIQUE "
		}
		b.WriteString("\n")
		b.WriteString(g.createIndexSQL(name, model.TableName(), unique, g.indexColumnsSQL(model, idx), idx.Where))
	}
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
	unique := ""
	if c.Details["unique"] == "true" {
		unique = "UNIQUE "
	}
	return g.createIndexSQL(name, c.Model, unique, g.indexColumnsFromDetails(c.Details), c.Details["where"])
}

func (g DDLGenerator) createIndexSQL(name, tableName, unique, columns, where string) string {
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
			Field:     dbField,
			Sort:      col.Sort,
			Nulls:     col.Nulls,
			OpClass:   col.OpClass,
			Collation: col.Collation,
		}))
	}
	return strings.Join(parts, ", ")
}

func (g DDLGenerator) indexColumnsFromDetails(details map[string]string) string {
	fields := splitDetailList(details["fields"])
	sorts := splitDetailList(details["sorts"])
	nulls := splitDetailList(details["nulls"])
	opclasses := splitDetailList(details["opclasses"])
	collations := splitDetailList(details["collations"])
	parts := make([]string, 0, len(fields))
	for i, field := range fields {
		parts = append(parts, g.indexColumnSQL(ir.IndexColumn{
			Field:     field,
			Sort:      detailAt(sorts, i),
			Nulls:     detailAt(nulls, i),
			OpClass:   detailAt(opclasses, i),
			Collation: detailAt(collations, i),
		}))
	}
	return strings.Join(parts, ", ")
}

func (g DDLGenerator) indexColumnSQL(col ir.IndexColumn) string {
	sql := g.quoteID(col.Field)
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
	if name == "" {
		name = fmt.Sprintf("uq_%s_%s", c.Model, c.Details["fields"])
	}
	if g.Dialect == "postgresql" {
		return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s);",
			g.quoteID(c.Model), g.quoteID(name), strings.Join(fields, ", "))
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
	case "postgresql":
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", g.quoteID(c.Model), g.quoteID(name))
	case "mysql":
		return fmt.Sprintf("DROP INDEX %s ON %s;", g.quoteID(name), g.quoteID(c.Model))
	default:
		return fmt.Sprintf("DROP INDEX IF EXISTS %s;", g.quoteID(name))
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
	constraintName := g.quoteID(fmt.Sprintf("fk_%s_%s", c.Model, c.Details["fields"]))

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
	others := make([]Change, 0)
	createTables := make([]Change, 0)
	addFKs := make([]Change, 0)
	dropTables := make([]Change, 0)

	for _, c := range cs.Changes {
		switch c.Type {
		case DropFK:
			dropFKs = append(dropFKs, c)
		case CreateTable:
			createTables = append(createTables, c)
		case AddFK:
			addFKs = append(addFKs, c)
		case DropTable:
			dropTables = append(dropTables, c)
		default:
			others = append(others, c)
		}
	}

	ordered := make([]Change, 0, len(cs.Changes))
	ordered = append(ordered, dropFKs...)
	ordered = append(ordered, others...)
	ordered = append(ordered, g.sortTableChanges(createTables, cs.New, false)...)
	ordered = append(ordered, addFKs...)
	ordered = append(ordered, g.sortTableChanges(dropTables, cs.Old, true)...)
	return ordered
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
	constraintName := g.quoteID(fmt.Sprintf("fk_%s_%s", c.Model, c.Details["fields"]))

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
	name := columnName(f)
	var parts []string
	parts = append(parts, g.quoteID(name))
	parts = append(parts, g.columnSQLType(f))

	if g.isAutoIncrementField(f) {
		parts = append(parts, g.autoIncrementClause())
	}

	if !f.IsOptional {
		parts = append(parts, "NOT NULL")
	}

	if f.IsUnique && !f.IsID {
		parts = append(parts, "UNIQUE")
	}

	if f.Default != nil && !g.isAutoIncrementField(f) {
		parts = append(parts, "DEFAULT "+g.defaultExprForField(f))
	}

	return strings.Join(parts, " ")
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
	return g.sqlTypeSignature(fieldTypeSignature(f))
}

func (g DDLGenerator) defaultExprForField(f *ir.Field) string {
	if f == nil || f.Default == nil {
		return "''"
	}
	if f.Default.IsArray {
		return g.arrayDefaultExpr(f.Default)
	}
	return g.defaultExpr(formatDefault(f.Default))
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
		parts := make([]string, len(values))
		copy(parts, values)
		return "'" + "[" + strings.Join(parts, ",") + "]" + "'"
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
			return "'" + val + "'"
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
