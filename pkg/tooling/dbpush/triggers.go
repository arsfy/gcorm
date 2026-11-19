package dbpush

import (
	"context"
	"strings"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/triggerddl"
)

func loadPostgresTriggers(ctx context.Context, db sqlRunner, schemaName string, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT c.relname, t.tgname, t.tgtype, p.proname, p.prosrc,
       t.tgenabled <> 'O' OR t.tgqual IS NOT NULL OR t.tgnargs <> 0 OR t.tgattr <> ''::int2vector
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid AND p.pronamespace = n.oid
WHERE n.nspname = $1 AND NOT t.tgisinternal
ORDER BY c.relname, t.tgname`, schemaName)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table, function string
		var flags int
		trigger := &ir.Trigger{}
		if err := rows.Scan(&table, &trigger.Name, &flags, &function, &trigger.Body, &trigger.NeedsReplacement); err != nil {
			return err
		}
		model := models[table]
		if model == nil || function != triggerddl.PostgresFunctionName(table, trigger.Name) || flags&1 == 0 {
			continue
		}
		// pg_trigger.tgtype stores row, before, insert, delete, and update
		// as successive bits. Only one operation is supported per definition.
		switch flags &^ 3 {
		case 4:
			trigger.Event = "INSERT"
		case 8:
			trigger.Event = "DELETE"
		case 16:
			trigger.Event = "UPDATE"
		default:
			continue
		}
		trigger.Timing = "AFTER"
		if flags&2 != 0 {
			trigger.Timing = "BEFORE"
		}
		trigger.Body = strings.TrimSpace(trigger.Body)
		model.Triggers = append(model.Triggers, trigger)
	}
	return rows.Err()
}

func loadMySQLTriggers(ctx context.Context, db sqlRunner, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT EVENT_OBJECT_TABLE, TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION, ACTION_STATEMENT
FROM information_schema.TRIGGERS
WHERE TRIGGER_SCHEMA = DATABASE()
ORDER BY EVENT_OBJECT_TABLE, TRIGGER_NAME`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table, action string
		trigger := &ir.Trigger{}
		if err := rows.Scan(&table, &trigger.Name, &trigger.Timing, &trigger.Event, &action); err != nil {
			return err
		}
		body, managed := triggerddl.MySQLBody(action)
		if model := models[table]; model != nil && managed {
			trigger.Body = body
			model.Triggers = append(model.Triggers, trigger)
		}
	}
	return rows.Err()
}

func loadSQLiteTriggers(ctx context.Context, db sqlRunner, schema *ir.Schema, models map[string]*ir.Model) error {
	rows, err := db.QueryContext(ctx, `
SELECT tbl_name, name, sql FROM main.sqlite_schema
WHERE type = 'trigger' AND sql IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var trigger ir.DatabaseTrigger
		if err := rows.Scan(&trigger.Table, &trigger.Name, &trigger.SQL); err != nil {
			return err
		}
		if model := models[trigger.Table]; model != nil {
			if managed := triggerddl.SQLiteDefinition(trigger.Name, trigger.SQL); managed != nil {
				model.Triggers = append(model.Triggers, managed)
				continue
			}
		}
		schema.UnmanagedTriggers = append(schema.UnmanagedTriggers, trigger)
	}
	return rows.Err()
}
