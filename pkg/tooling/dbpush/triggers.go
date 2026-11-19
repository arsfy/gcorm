package dbpush

import (
	"context"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/triggerddl"
)

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
