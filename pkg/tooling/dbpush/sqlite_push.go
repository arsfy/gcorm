package dbpush

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
)

func pushCompiledSQLite(ctx context.Context, db *sql.DB, target *ir.Schema, hash, dsn string, opts Options) (result *Result, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire SQLite connection: %w", err)
	}
	defer conn.Close()

	var foreignKeys, legacyAlterTable bool
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return nil, fmt.Errorf("read SQLite foreign_keys: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA legacy_alter_table").Scan(&legacyAlterTable); err != nil {
		return nil, fmt.Errorf("read SQLite legacy_alter_table: %w", err)
	}
	defer func() {
		// These settings belong to the pooled connection. Restore them even
		// when cancellation has already caused the transaction to roll back.
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, fkErr := conn.ExecContext(restoreCtx, sqliteSetPragma("foreign_keys", foreignKeys))
		_, alterErr := conn.ExecContext(restoreCtx, sqliteSetPragma("legacy_alter_table", legacyAlterTable))
		if restoreErr := errors.Join(fkErr, alterErr); restoreErr != nil {
			// Prevent another borrower from receiving altered connection state.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, fmt.Errorf("restore SQLite connection settings: %w", restoreErr))
		}
	}()

	// DROP TABLE fires FK actions even with deferred checks. Enforcement must
	// be disabled before BEGIN on this connection and checked before COMMIT.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return nil, fmt.Errorf("disable SQLite foreign keys during schema application: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA legacy_alter_table = ON"); err != nil {
		return nil, fmt.Errorf("configure SQLite table rename: %w", err)
	}
	return pushTransactional(ctx, conn, "sqlite", target, hash, dsn, opts, foreignKeys)
}

func sqliteSetPragma(name string, on bool) string {
	if on {
		return "PRAGMA " + name + " = ON"
	}
	return "PRAGMA " + name + " = OFF"
}

func lockSQLitePush(ctx context.Context, tx *sql.Tx) error {
	if err := ensureSchemaPushesTable(ctx, tx, "sqlite"); err != nil {
		return err
	}
	// CREATE TABLE IF NOT EXISTS only takes a read lock once the table
	// exists. Force a write transaction before introspecting the schema.
	if _, err := tx.ExecContext(ctx, "UPDATE main.__gco_schema_pushes SET id = id WHERE 0"); err != nil {
		return fmt.Errorf("acquire SQLite db push lock: %w", err)
	}
	return nil
}

func checkSQLiteForeignKeys(ctx context.Context, tx *sql.Tx) error {
	var table, parent string
	var rowID sql.NullInt64
	var constraint int
	err := tx.QueryRowContext(ctx, "SELECT * FROM pragma_foreign_key_check LIMIT 1").Scan(&table, &rowID, &parent, &constraint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check SQLite foreign keys: %w", err)
	}
	row := ""
	if rowID.Valid {
		row = fmt.Sprintf(" (rowid %d)", rowID.Int64)
	}
	return fmt.Errorf("SQLite foreign key check failed: %q%s references %q (constraint %d)", table, row, parent, constraint)
}

func sqliteChangesTableShape(cs *migrate.Changeset) bool {
	if cs.RebuildsSQLiteTables() {
		return true
	}
	for _, change := range cs.Changes {
		switch change.Type {
		case migrate.AddColumn, migrate.DropTable, migrate.RenameColumn, migrate.RenameTable:
			return true
		}
	}
	return false
}

func validSQLiteViews(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM main.sqlite_schema WHERE type = 'view' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var valid []string
	for _, name := range names {
		if err := prepareSQLiteView(ctx, tx, name); err == nil {
			valid = append(valid, name)
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return valid, nil
}

func prepareSQLiteView(ctx context.Context, tx *sql.Tx, name string) error {
	rows, err := tx.QueryContext(ctx, "SELECT * FROM main."+sqliteQuoteIdent(name)+" LIMIT 0")
	if err != nil {
		return err
	}
	return rows.Close()
}

func checkSQLiteTriggerActions(ctx context.Context, tx *sql.Tx, schema *ir.Schema) error {
	for _, model := range schema.Models {
		table := "main." + sqliteQuoteIdent(model.TableName())
		checked := make(map[string]bool)
		for _, trigger := range model.Triggers {
			if checked[trigger.Event] {
				continue
			}
			checked[trigger.Event] = true
			var query string
			switch trigger.Event {
			case "INSERT":
				query = "EXPLAIN INSERT INTO " + table + " DEFAULT VALUES"
			case "DELETE":
				query = "EXPLAIN DELETE FROM " + table
			case "UPDATE":
				var assignments []string
				for _, field := range model.ScalarFields() {
					column := sqliteQuoteIdent(sqliteFieldColumnName(field))
					assignments = append(assignments, column+" = "+column)
				}
				query = "EXPLAIN UPDATE " + table + " SET " + strings.Join(assignments, ", ")
			}
			if query == "" {
				continue
			}
			// EXPLAIN compiles trigger bodies without writing any rows.
			rows, err := tx.QueryContext(ctx, query)
			if err != nil {
				return fmt.Errorf("validate SQLite %s triggers on %q: %w", trigger.Event, model.TableName(), err)
			}
			if err := rows.Close(); err != nil {
				return fmt.Errorf("validate SQLite %s triggers on %q: %w", trigger.Event, model.TableName(), err)
			}
		}
	}
	return nil
}
