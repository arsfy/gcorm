package migrate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/triggerddl"
)

// postgresPreludeSteps returns the PostgreSQL steps that run before table
// changes: enum renames and added values (RequiresCommit), then new schemas,
// new enum types, and enum types recreated to remove values.
func (g DDLGenerator) postgresPreludeSteps(cs *Changeset) []DDLStep {
	var commit, steps []DDLStep
	for _, name := range newModelSchemas(cs) {
		steps = append(steps, DDLStep{SQL: "CREATE SCHEMA IF NOT EXISTS " + g.quoteID(name) + ";"})
	}
	for _, c := range cs.Changes {
		switch c.Type {
		case CreateEnum:
			if enum := findEnum(cs.New, c.Model); enum != nil {
				steps = append(steps, DDLStep{SQL: g.postgresCreateEnumSQL(enum)})
			}
		case RenameEnum:
			commit = append(commit, DDLStep{SQL: g.postgresRenameEnumSQL(c.OldValue, c.NewValue), RequiresCommit: true})
		case AlterEnum:
			from, to := findEnum(cs.Old, c.Model), findEnum(cs.New, c.Model)
			if from == nil || to == nil {
				continue
			}
			enumCommit, enumSteps := g.postgresEnumTransition(enumTypeName(to), from, to, enumColumnsForUp(cs, c.Model))
			commit = append(commit, enumCommit...)
			steps = append(steps, enumSteps...)
		}
	}
	return append(commit, steps...)
}

// newModelSchemas returns the non-empty target model schemas that no source
// model uses, in name order.
func newModelSchemas(cs *Changeset) []string {
	if cs.New == nil {
		return nil
	}
	existing := make(map[string]bool)
	if cs.Old != nil {
		for _, m := range cs.Old.Models {
			existing[m.Schema] = true
		}
	}
	var names []string
	for _, m := range cs.New.Models {
		if m.Schema != "" && !existing[m.Schema] {
			existing[m.Schema] = true
			names = append(names, m.Schema)
		}
	}
	sort.Strings(names)
	return names
}

// postgresCreateEnumSQL creates an enum type. It fails if the type exists so
// that drift between the migration history and the database surfaces.
func (g DDLGenerator) postgresCreateEnumSQL(e *ir.Enum) string {
	return g.postgresCreateEnumTypeSQL(enumTypeName(e), e)
}

func (g DDLGenerator) postgresCreateEnumTypeSQL(name string, e *ir.Enum) string {
	values := make([]string, len(e.Values))
	for i, v := range e.Values {
		values[i] = quoteSQLString(enumValueLabel(v))
	}
	return "CREATE TYPE " + g.quoteID(name) + " AS ENUM (" + strings.Join(values, ", ") + ");"
}

func (g DDLGenerator) postgresRenameEnumSQL(from, to string) string {
	return "ALTER TYPE " + g.quoteID(from) + " RENAME TO " + g.quoteID(to) + ";"
}

// enumColumn is a column that uses an enum type while the type changes.
type enumColumn struct {
	schema, table string
	field         *ir.Field
	// restore is the column definition whose default is set again after the
	// column is converted to a recreated type, or nil.
	restore *ir.Field
}

// sourceEnumFields calls fn for each source column that uses enumName.
func sourceEnumFields(cs *Changeset, enumName string, fn func(m *ir.Model, f *ir.Field)) {
	if cs.Old == nil {
		return
	}
	for _, m := range cs.Old.Models {
		for _, f := range m.ScalarFields() {
			if f.Type == ir.FieldKindEnum && f.EnumType == enumName {
				fn(m, f)
			}
		}
	}
}

// enumColumnsForUp lists the columns of an enum while its type changes.
// Enum changes run before table changes, so every source column exists at
// its source location. The target default is restored when the target
// column still uses the enum.
func enumColumnsForUp(cs *Changeset, enumName string) []enumColumn {
	var columns []enumColumn
	sourceEnumFields(cs, enumName, func(m *ir.Model, f *ir.Field) {
		column := enumColumn{schema: m.Schema, table: m.TableName(), field: f}
		if restore := findTableField(cs.New, m.Schema, m.TableName(), columnName(f)); restore != nil &&
			restore.Type == ir.FieldKindEnum && restore.EnumType == enumName && restore.IsList == f.IsList {
			column.restore = restore
		}
		columns = append(columns, column)
	})
	return columns
}

// enumColumnsForDown lists the columns of an enum while a down migration
// restores its type. That runs after table changes are reverted, but before
// tables move back to their source schemas. Columns and tables dropped by the
// migration are not restored.
func enumColumnsForDown(cs *Changeset, enumName string) []enumColumn {
	var columns []enumColumn
	sourceEnumFields(cs, enumName, func(m *ir.Model, f *ir.Field) {
		target := findTableModel(cs.New, m.Schema, m.TableName())
		if fieldByName(target, columnName(f)) == nil {
			return
		}
		columns = append(columns, enumColumn{schema: target.Schema, table: m.TableName(), field: f, restore: f})
	})
	return columns
}

// postgresEnumTransition changes the enum type currently named typeName
// from one value list to another. Value @map changes become RENAME VALUE.
// Added values use ADD VALUE positioned to keep the declared order. Removing
// a value recreates the type, because PostgreSQL cannot drop enum labels.
func (g DDLGenerator) postgresEnumTransition(typeName string, from, to *ir.Enum, columns []enumColumn) (commit, steps []DDLStep) {
	quotedType := g.quoteID(typeName)
	for _, rename := range enumLabelRenames(from, to) {
		commit = append(commit, DDLStep{
			SQL:            fmt.Sprintf("ALTER TYPE %s RENAME VALUE %s TO %s;", quotedType, quoteSQLString(rename[0]), quoteSQLString(rename[1])),
			RequiresCommit: true,
		})
	}
	if len(removedEnumLabels(from, to)) > 0 {
		return commit, []DDLStep{{SQL: g.postgresRecreateEnumSQL(typeName, to, columns)}}
	}
	present := make(map[string]bool)
	for _, label := range enumLabelsAfterRenames(from, to) {
		present[label] = true
	}
	targetLabels := make([]string, len(to.Values))
	for i, v := range to.Values {
		targetLabels[i] = enumValueLabel(v)
	}
	for i, label := range targetLabels {
		if present[label] {
			continue
		}
		stmt := fmt.Sprintf("ALTER TYPE %s ADD VALUE IF NOT EXISTS %s", quotedType, quoteSQLString(label))
		if i > 0 {
			stmt += " AFTER " + quoteSQLString(targetLabels[i-1])
		} else {
			for _, next := range targetLabels[1:] {
				if present[next] {
					stmt += " BEFORE " + quoteSQLString(next)
					break
				}
			}
		}
		commit = append(commit, DDLStep{SQL: stmt + ";", RequiresCommit: true})
		present[label] = true
	}
	return commit, steps
}

// postgresRecreateEnumSQL replaces the enum type with one holding the target
// values: the old type is renamed, the new type created, each dependent
// column converted through text, and the old type dropped. Rows holding a
// removed value make the conversion fail. Column defaults are dropped before
// the conversion and the target default restored afterwards.
func (g DDLGenerator) postgresRecreateEnumSQL(typeName string, to *ir.Enum, columns []enumColumn) string {
	oldName := LimitIdentifier(typeName + "__gco_old")
	statements := []string{
		g.postgresRenameEnumSQL(typeName, oldName),
		g.postgresCreateEnumTypeSQL(typeName, to),
	}
	for _, column := range columns {
		tbl := g.tableRef(column.schema, column.table)
		col := g.quoteID(columnName(column.field))
		newType := g.quoteID(typeName)
		if column.field.IsList {
			newType += "[]"
		}
		dropsDefault := g.hasDatabaseDefault(column.field)
		if dropsDefault {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, col))
		}
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::text::%s;", tbl, col, newType, col, newType))
		if dropsDefault && g.hasDatabaseDefault(column.restore) {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tbl, col, g.defaultExprForField(column.restore)))
		}
	}
	statements = append(statements, "DROP TYPE "+g.quoteID(oldName)+";")
	return strings.Join(statements, "\n")
}

// postgresRevertEnumSQL restores the source values of an altered enum. It
// runs before the enum's RenameEnum is reverted, so the type still has its
// target name.
func (g DDLGenerator) postgresRevertEnumSQL(c Change, cs *Changeset) string {
	from, to := findEnum(cs.New, c.Model), findEnum(cs.Old, c.Model)
	if from == nil || to == nil {
		return "-- REVIEW REQUIRED: cannot revert enum change for " + c.Model + " without both schemas"
	}
	commit, steps := g.postgresEnumTransition(enumTypeName(from), from, to, enumColumnsForDown(cs, c.Model))
	var script []string
	if len(steps) > 0 {
		script = append(script, "-- REVIEW REQUIRED: recreates enum type "+g.quoteID(enumTypeName(from))+"; rows holding values added by the migration make this fail.")
	}
	if len(commit) > 0 {
		script = append(script, requiresCommitStartComment)
		for _, step := range commit {
			script = append(script, step.SQL)
		}
		script = append(script, requiresCommitEndComment)
	}
	for _, step := range steps {
		script = append(script, step.SQL)
	}
	return strings.Join(script, "\n")
}

// setTableSchemaStep moves a table between schemas together with the
// functions of its unchanged managed triggers, which live in the table's
// schema. Indexes, constraints, and identity sequences move with the table.
// An empty target schema means the first schema on search_path.
func (g DDLGenerator) setTableSchemaStep(table, fromSchema, toSchema string, fromIR, toIR *ir.Schema) DDLStep {
	var functions []string
	fromModel, toModel := findTableModel(fromIR, fromSchema, table), findTableModel(toIR, toSchema, table)
	if fromModel != nil && toModel != nil {
		kept := indexTriggers(toModel)
		for _, trigger := range fromModel.Triggers {
			if other := kept[trigger.Name]; other != nil && *other == *trigger {
				functions = append(functions, triggerddl.PostgresFunctionName(table, trigger.Name))
			}
		}
	}
	if toSchema != "" {
		target := g.quoteID(toSchema)
		statements := []string{fmt.Sprintf("ALTER TABLE %s SET SCHEMA %s;", g.tableRef(fromSchema, table), target)}
		for _, function := range functions {
			statements = append(statements, fmt.Sprintf("ALTER FUNCTION %s() SET SCHEMA %s;", g.qualifiedName(fromSchema, function), target))
		}
		return DDLStep{SQL: strings.Join(statements, "\n")}
	}
	var body strings.Builder
	fmt.Fprintf(&body, "  EXECUTE format('ALTER TABLE %%I.%%I SET SCHEMA %%I', %s, %s, current_schema());\n",
		quoteSQLString(fromSchema), quoteSQLString(table))
	for _, function := range functions {
		fmt.Fprintf(&body, "  EXECUTE format('ALTER FUNCTION %%I.%%I() SET SCHEMA %%I', %s, %s, current_schema());\n",
			quoteSQLString(fromSchema), quoteSQLString(function))
	}
	tag := "$gco$"
	for n := 1; strings.Contains(body.String(), tag); n++ {
		tag = "$gco" + strconv.Itoa(n) + "$"
	}
	return DDLStep{SQL: "DO " + tag + "\nBEGIN\n" + body.String() + "END\n" + tag + ";", SingleStatement: true}
}

// movedForeignKeyTarget points a restored foreign key at the target table's
// location during a down migration, which reverts table moves only after
// foreign keys are restored.
func movedForeignKeyTarget(cs *Changeset, details map[string]string) map[string]string {
	for _, c := range cs.Changes {
		if c.Type != SetTableSchema || c.Model != details["toModel"] || c.OldValue != details["toSchema"] {
			continue
		}
		moved := make(map[string]string, len(details))
		for k, v := range details {
			moved[k] = v
		}
		moved["toSchema"] = c.NewValue
		return moved
	}
	return details
}
