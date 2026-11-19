package migrate

import (
	"fmt"
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
	header := fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH ROW",
		g.quoteID(trigger.Name), trigger.Timing, trigger.Event, g.tableRef(schemaName, table))
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
				restore = append(restore, g.createTriggerSteps("", model.TableName(), trigger)...)
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
