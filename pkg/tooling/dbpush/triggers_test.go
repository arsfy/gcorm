package dbpush

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arsfy/gcorm/pkg/schema/ast"
	"github.com/arsfy/gcorm/pkg/schema/formatter"
	"github.com/arsfy/gcorm/pkg/tooling/internal/triggerddl"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPushTriggerLifecycle(t *testing.T) {
	for _, provider := range []string{"sqlite", "postgresql", "mysql"} {
		t.Run(provider, func(t *testing.T) {
			db, dsn, schema := openTriggerTestDatabase(t, provider)
			table := "items"
			logTable := "audit_log"
			if provider == "postgresql" {
				table, logTable = schema+"."+table, schema+"."+logTable
			}
			// Include text containing a semicolon, a SQL comment marker, and
			// the default MySQL and PostgreSQL script delimiters.
			const message = "first;  -- // $gco$"
			body := "INSERT INTO audit_log (id, message) VALUES (NEW.id, '" + message + "');\n" +
				"UPDATE audit_log SET message = CONCAT(message, '!') WHERE id = NEW.id;"
			if provider == "sqlite" {
				body = strings.ReplaceAll(body, "CONCAT(message, '!')", "message || '!'")
			}
			if provider == "postgresql" {
				body = "BEGIN\n" + body + "\nRETURN NEW;\nEND;"
			}
			content := triggerSchema(provider, schema, "note String?", "AFTER", "INSERT", body)

			plan, err := Push(t.Context(), nil, Options{SchemaFS: triggerFS(content), DryRun: true, SkipIntrospection: true})
			if err != nil {
				t.Fatal(err)
			}
			var creates int
			for _, stmt := range plan.Statements {
				if strings.HasPrefix(stmt, "CREATE TRIGGER ") {
					creates++
				}
				if strings.HasPrefix(stmt, "DELIMITER ") {
					t.Fatalf("driver plan contains client command: %q", stmt)
				}
			}
			if creates != 1 {
				t.Fatalf("planned %d CREATE TRIGGER statements: %v", creates, plan.Statements)
			}

			pushTriggerSchema(t, db, dsn, content, false)
			requireTriggerNoop(t, db, dsn, content)
			if provider == "postgresql" {
				// A manually removed trigger can leave its function behind.
				execTriggerSQL(t, db, "DROP TRIGGER item_audit ON "+table)
				pushTriggerSchema(t, db, dsn, content, false)
				requireTriggerNoop(t, db, dsn, content)

				execTriggerSQL(t, db, "ALTER TABLE "+table+" DISABLE TRIGGER item_audit")
				_, err := Push(t.Context(), db, Options{SchemaFS: triggerFS(content), DatabaseURL: dsn})
				if err == nil || !strings.Contains(err.Error(), "DropTrigger") {
					t.Fatalf("repair of disabled trigger without force returned %v", err)
				}
				pushTriggerSchema(t, db, dsn, content, true)
				requireTriggerNoop(t, db, dsn, content)
			}
			execTriggerSQL(t, db, "INSERT INTO "+table+" (id) VALUES (1)")
			requireAuditMessage(t, db, logTable, 1, message+"!")

			replacement := strings.ReplaceAll(body, message, "changed")
			changed := triggerSchema(provider, schema, "note String?", "BEFORE", "UPDATE", replacement)
			_, err = Push(t.Context(), db, Options{SchemaFS: triggerFS(changed), DatabaseURL: dsn})
			if err == nil || !strings.Contains(err.Error(), "DropTrigger") {
				t.Fatalf("replacement without force error = %v", err)
			}
			execTriggerSQL(t, db, "INSERT INTO "+table+" (id) VALUES (2)")
			requireAuditMessage(t, db, logTable, 2, message+"!")
			pushTriggerSchema(t, db, dsn, changed, true)
			requireTriggerNoop(t, db, dsn, changed)
			execTriggerSQL(t, db, "INSERT INTO "+table+" (id) VALUES (3)")
			requireAuditMessage(t, db, logTable, 3, "")
			execTriggerSQL(t, db, "UPDATE "+table+" SET note = 'x' WHERE id = 3")
			requireAuditMessage(t, db, logTable, 3, "changed!")

			brokenBody := "INVALID SQL;"
			if provider == "postgresql" {
				brokenBody = "BEGIN\n" + brokenBody + "\nRETURN NEW;\nEND;"
			}
			broken := triggerSchema(provider, schema, "note String?", "BEFORE", "UPDATE", brokenBody)
			failed, err := Push(t.Context(), db, Options{
				SchemaFS: triggerFS(broken), DatabaseURL: dsn, AllowDestructive: true,
			})
			if err == nil {
				t.Fatal("replacement succeeded with invalid SQL")
			}
			if provider == "mysql" {
				if failed == nil || len(failed.AppliedStatements) != 1 {
					t.Fatalf("MySQL did not report the committed drop: %+v", failed)
				}
				pushTriggerSchema(t, db, dsn, changed, false)
			}
			requireTriggerNoop(t, db, dsn, changed)

			deletionBody := strings.NewReplacer("NEW.", "OLD.", "RETURN NEW;", "RETURN OLD;").Replace(replacement)
			deletion := triggerSchema(provider, schema, "note String?", "AFTER", "DELETE", deletionBody)
			pushTriggerSchema(t, db, dsn, deletion, true)
			requireTriggerNoop(t, db, dsn, deletion)
			execTriggerSQL(t, db, "INSERT INTO "+table+" (id) VALUES (4)")
			requireAuditMessage(t, db, logTable, 4, "")
			execTriggerSQL(t, db, "DELETE FROM "+table+" WHERE id = 4")
			requireAuditMessage(t, db, logTable, 4, "changed!")

			removed := triggerSchema(provider, schema, "note String?", "", "", "")
			pushTriggerSchema(t, db, dsn, removed, true)
			requireTriggerNoop(t, db, dsn, removed)
			execTriggerSQL(t, db, "INSERT INTO "+table+" (id) VALUES (5)")
			execTriggerSQL(t, db, "UPDATE "+table+" SET note = 'x' WHERE id = 5")
			execTriggerSQL(t, db, "DELETE FROM "+table+" WHERE id = 5")
			requireAuditMessage(t, db, logTable, 5, "")
			if provider == "postgresql" {
				var count int
				err := db.QueryRow(`SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2`, schema, triggerddl.PostgresFunctionName("items", "item_audit")).Scan(&count)
				if err != nil || count != 0 {
					t.Fatalf("helper function count = %d, error = %v", count, err)
				}
			}
		})
	}
}

func TestPushSQLiteRebuildPreservesTriggers(t *testing.T) {
	db, dsn := openTestSQLite(t)
	const body = `INSERT INTO audit_log (id, message) VALUES (NEW.id, 'managed');`
	original := triggerSchema("sqlite", "", "note String?", "AFTER", "INSERT", body)
	pushTriggerSchema(t, db, dsn, original, false)
	execTriggerSQL(t, db, `CREATE TRIGGER native_item_delete AFTER DELETE ON items
BEGIN INSERT INTO audit_log (id, message) VALUES (OLD.id + 100, 'native'); END;`)
	execTriggerSQL(t, db, `CREATE TRIGGER native_audit_delete AFTER DELETE ON audit_log
BEGIN DELETE FROM items WHERE id = OLD.id; END;`)
	execTriggerSQL(t, db, "INSERT INTO items (id, note) VALUES (1, 'remove')")
	requireTriggerNoop(t, db, dsn, original)

	rebuilt := triggerSchema("sqlite", "", "", "AFTER", "INSERT", body)
	pushTriggerSchema(t, db, dsn, rebuilt, true)
	requireTriggerNoop(t, db, dsn, rebuilt)
	requireAuditMessage(t, db, "audit_log", 1, "managed")
	execTriggerSQL(t, db, "DELETE FROM audit_log WHERE id = 1")
	requireAuditMessage(t, db, "audit_log", 101, "native")
	execTriggerSQL(t, db, "INSERT INTO items (id) VALUES (2)")
	requireAuditMessage(t, db, "audit_log", 2, "managed")
}

func TestPushSQLiteFailedTriggerReplacementRollsBack(t *testing.T) {
	db, dsn := openTestSQLite(t)
	const body = `INSERT INTO audit_log (id, message) VALUES (NEW.id, 'original');`
	original := triggerSchema("sqlite", "", "", "AFTER", "INSERT", body)
	pushTriggerSchema(t, db, dsn, original, false)
	broken := triggerSchema("sqlite", "", "", "AFTER", "INSERT", "INVALID SQL;")
	_, err := Push(t.Context(), db, Options{SchemaFS: triggerFS(broken), DatabaseURL: dsn, AllowDestructive: true})
	if err == nil {
		t.Fatal("push succeeded with invalid SQLite syntax")
	}
	requireTriggerNoop(t, db, dsn, original)
	execTriggerSQL(t, db, "INSERT INTO items (id) VALUES (1)")
	requireAuditMessage(t, db, "audit_log", 1, "original")
}

func openTriggerTestDatabase(t *testing.T, provider string) (*sql.DB, string, string) {
	t.Helper()
	if provider == "sqlite" {
		db, dsn := openTestSQLite(t)
		return db, dsn, ""
	}
	var env string
	if provider == "postgresql" {
		env = "GCORM_TEST_POSTGRES_URL"
	} else {
		env = "GCORM_TEST_MYSQL_DSN"
	}
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("set %s to run this integration test", env)
	}
	admin, err := sql.Open(driverName(provider), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	name := fmt.Sprintf("gcorm_triggers_%d", time.Now().UnixNano())
	if provider == "postgresql" {
		execTriggerSQL(t, admin, "CREATE SCHEMA "+name)
		t.Cleanup(func() {
			if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+name+" CASCADE"); err != nil {
				t.Error(err)
			}
		})
		return admin, dsn, name
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	execTriggerSQL(t, admin, "CREATE DATABASE "+name)
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, cfg.FormatDSN(), ""
}

func triggerSchema(provider, schema, column, timing, event, body string) string {
	var extra, declaration string
	if schema != "" {
		extra = fmt.Sprintf("  schema = %q\n", schema)
	}
	if body != "" {
		declaration = fmt.Sprintf("  @@trigger(name: \"item_audit\", timing: %s, event: %s, body: %s)\n",
			timing, event, formatter.FormatExpression(ast.StringLiteral{Value: body}))
	}
	return fmt.Sprintf(`datasource db {
  provider = %q
  url      = "unused"
%s}
model Item {
  id Int @id
  %s
  @@map("items")
%s}
model AuditLog {
  id Int @id
  message String
  @@map("audit_log")
}
`, provider, extra, column, declaration)
}

func triggerFS(content string) fstest.MapFS {
	return fstest.MapFS{"schema.gcorm": {Data: []byte(content)}}
}

func pushTriggerSchema(t *testing.T, db *sql.DB, dsn, content string, force bool) *Result {
	t.Helper()
	result, err := Push(t.Context(), db, Options{SchemaFS: triggerFS(content), DatabaseURL: dsn, AllowDestructive: force})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	return result
}

func requireTriggerNoop(t *testing.T, db *sql.DB, dsn, content string) {
	t.Helper()
	if result := pushTriggerSchema(t, db, dsn, content, false); !result.Noop {
		t.Fatalf("push did not converge: %+v", result)
	}
}

func execTriggerSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query); err != nil {
		t.Fatalf("execute %q: %v", query, err)
	}
}

func requireAuditMessage(t *testing.T, db *sql.DB, table string, id int, want string) {
	t.Helper()
	var got string
	err := db.QueryRowContext(t.Context(), "SELECT message FROM "+table+fmt.Sprintf(" WHERE id = %d", id)).Scan(&got)
	if want == "" && err == sql.ErrNoRows {
		return
	}
	if err != nil || got != want {
		t.Fatalf("audit message for id %d = %q, error = %v, want %q", id, got, err, want)
	}
}
