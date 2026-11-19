package migrate

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/triggerddl"
)

// createTriggerSteps creates a managed trigger. On PostgreSQL its function
// lives in the table's schema.
func (g DDLGenerator) createTriggerSteps(schemaName, table string, trigger *ir.Trigger) []DDLStep {
	if trigger == nil {
		return nil
	}
	event := trigger.Event
	if len(trigger.Columns) > 0 {
		event += " OF " + strings.Join(g.quoteIDs(trigger.Columns), ", ")
	}
	header := fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH ROW",
		g.quoteID(trigger.Name), trigger.Timing, event, g.tableRef(schemaName, table))
	switch g.Dialect {
	case "postgresql":
		name := g.qualifiedName(schemaName, triggerddl.PostgresFunctionName(table, trigger.Name)) + "()"
		tag := "$gco$"
		for n := 1; strings.Contains(trigger.Body, tag); n++ {
			tag = "$gco" + strconv.Itoa(n) + "$"
		}
		function := fmt.Sprintf("CREATE OR REPLACE FUNCTION %s RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS %s\n%s\n%s;",
			name, tag, trigger.Body, tag)
		return []DDLStep{
			{SQL: function, SingleStatement: true},
			{SQL: header + " EXECUTE FUNCTION " + name + ";"},
		}
	case "mysql":
		return []DDLStep{{SQL: header + "\n" + triggerddl.MySQLBegin + "\n" + trigger.Body + "\nEND;", SingleStatement: true}}
	case "sqlite":
		return []DDLStep{{SQL: header + "\n" + triggerddl.SQLiteBegin + "\n" + trigger.Body + "\nEND;", SingleStatement: true}}
	}
	return nil
}

func (g DDLGenerator) dropTriggerSQL(schemaName, table, name string) string {
	sql := "DROP TRIGGER IF EXISTS " + g.quoteID(name)
	switch g.Dialect {
	case "postgresql":
		sql += " ON " + g.tableRef(schemaName, table) + ";\nDROP FUNCTION IF EXISTS " + g.qualifiedName(schemaName, triggerddl.PostgresFunctionName(table, name)) + "()"
	case "sqlite":
		sql = "DROP TRIGGER IF EXISTS main." + g.quoteID(name)
	}
	return sql + ";"
}

func (g DDLGenerator) scriptSQL(step DDLStep) string {
	if g.Dialect != "mysql" || !step.SingleStatement {
		return step.SQL
	}
	delimiter := "//"
	for n := 1; strings.Contains(step.SQL, delimiter); n++ {
		delimiter = "~gco" + strconv.Itoa(n) + "~"
	}
	return "DELIMITER " + delimiter + "\n" + strings.TrimSuffix(step.SQL, ";") + delimiter + "\nDELIMITER ;"
}

func (g DDLGenerator) sqliteRebuildTriggers(cs *Changeset, tables map[string]bool) []DDLStep {
	if cs.Old == nil || len(tables) == 0 {
		return nil
	}
	dropping := make(map[string]bool)
	for _, change := range cs.Changes {
		if change.Type == DropTrigger {
			dropping[change.Field] = true
		}
	}

	// SQLite drops triggers attached to the replaced table. Other triggers
	// and views retain their references with legacy_alter_table enabled.
	var restore []DDLStep
	for _, model := range cs.Old.Models {
		if !tables[model.TableName()] {
			continue
		}
		for _, trigger := range model.Triggers {
			if !dropping[trigger.Name] {
				restore = append(restore, g.createTriggerSteps("", model.TableName(), normalizedTrigger(model, trigger))...)
			}
		}
	}
	for _, trigger := range cs.Old.UnmanagedTriggers {
		if tables[trigger.Table] {
			sql := strings.TrimSuffix(strings.TrimSpace(trigger.SQL), ";") + ";"
			restore = append(restore, DDLStep{SQL: sql, SingleStatement: true})
		}
	}
	return restore
}

// normalizedTrigger returns trigger with its UPDATE OF columns resolved from
// field names to column names of model, ready for comparison and DDL.
func normalizedTrigger(model *ir.Model, trigger *ir.Trigger) *ir.Trigger {
	if trigger == nil || len(trigger.Columns) == 0 {
		return trigger
	}
	clone := *trigger
	clone.Columns = normalizeFieldNames(model, trigger.Columns)
	return &clone
}

// triggersEqual compares normalized triggers. The UPDATE OF column list is a
// set: its order does not change when the trigger fires.
func triggersEqual(a, b *ir.Trigger) bool {
	if a.Name != b.Name || a.Timing != b.Timing || a.Event != b.Event || a.Body != b.Body || a.NeedsReplacement != b.NeedsReplacement {
		return false
	}
	ac, bc := slices.Clone(a.Columns), slices.Clone(b.Columns)
	slices.Sort(ac)
	slices.Sort(bc)
	return slices.Equal(ac, bc)
}
