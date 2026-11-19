package dbpush

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestPushSQLiteRebuildKeepsChildrenSequenceAndView(t *testing.T) {
	db, dsn := openTestSQLite(t)
	db.SetMaxOpenConns(1)
	execTriggerSQL(t, db, "PRAGMA foreign_keys = ON")
	original := sqliteParentSchema("note String?", true)
	pushTriggerSchema(t, db, dsn, original, false)
	execTriggerSQL(t, db, "INSERT INTO parents (id, note) VALUES (1, 'kept'), (90, 'retired')")
	execTriggerSQL(t, db, "DELETE FROM parents WHERE id = 90")
	execTriggerSQL(t, db, "INSERT INTO children (id, parent_id) VALUES (10, 1)")
	execTriggerSQL(t, db, "CREATE VIEW parent_ids AS SELECT id FROM parents")
	execTriggerSQL(t, db, `CREATE TRIGGER insert_parent_view INSTEAD OF INSERT ON parent_ids
BEGIN INSERT INTO parents (id) VALUES (NEW.id); END`)

	rebuilt := sqliteParentSchema("createdAt DateTime @default(now())", true)
	pushTriggerSchema(t, db, dsn, rebuilt, true)
	requireTriggerNoop(t, db, dsn, rebuilt)
	checkSQLiteInt(t, db, "SELECT parent_id FROM children WHERE id = 10", 1)
	checkSQLiteInt(t, db, "SELECT count(*) FROM parents WHERE created_at IS NOT NULL", 1)
	checkSQLiteInt(t, db, "SELECT id FROM parent_ids", 1)
	execTriggerSQL(t, db, "INSERT INTO parent_ids DEFAULT VALUES")
	checkSQLiteInt(t, db, "SELECT max(id) FROM parent_ids", 91)
	checkSQLiteInt(t, db, "PRAGMA foreign_keys", 1)
	checkSQLiteInt(t, db, "PRAGMA legacy_alter_table", 0)
	if _, err := db.ExecContext(t.Context(), "INSERT INTO children (id, parent_id) VALUES (11, 999)"); err == nil {
		t.Fatal("foreign keys not enforced after returning the connection")
	}
}

func TestPushSQLiteRebuildRollsBackNewForeignKeyViolations(t *testing.T) {
	db, dsn := openTestSQLite(t)
	db.SetMaxOpenConns(1)
	execTriggerSQL(t, db, "PRAGMA foreign_keys = ON")
	execTriggerSQL(t, db, "PRAGMA legacy_alter_table = ON")
	original := sqliteParentSchema("", false)
	pushTriggerSchema(t, db, dsn, original, false)
	execTriggerSQL(t, db, "INSERT INTO children (id, parent_id) VALUES (1, 999)")

	_, err := Push(t.Context(), db, Options{
		SchemaFS: triggerFS(sqliteParentSchema("", true)), DatabaseURL: dsn,
	})
	if err == nil || !strings.Contains(err.Error(), `SQLite foreign key check failed: "children"`) {
		t.Fatalf("push error = %v, want foreign key validation error", err)
	}
	checkSQLiteInt(t, db, "SELECT parent_id FROM children", 999)
	checkSQLiteInt(t, db, "SELECT count(*) FROM pragma_foreign_key_list('children')", 0)
	checkSQLiteInt(t, db, "PRAGMA foreign_keys", 1)
	checkSQLiteInt(t, db, "PRAGMA legacy_alter_table", 1)
	requireTriggerNoop(t, db, dsn, original)
}

func TestPushSQLiteRebuildRejectsBrokenViewOrTrigger(t *testing.T) {
	for _, tt := range []struct {
		name, create, timing, event, body, want string
	}{
		{
			name: "view", create: "CREATE VIEW item_notes AS SELECT note FROM items",
			want: `would break view "item_notes"`,
		},
		{
			name: "trigger", timing: "AFTER", event: "INSERT", body: "SELECT NEW.note;",
			want: "no such column: NEW.note",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, dsn := openTestSQLite(t)
			original := triggerSchema("sqlite", "", "note String?", tt.timing, tt.event, tt.body)
			pushTriggerSchema(t, db, dsn, original, false)
			if tt.create != "" {
				execTriggerSQL(t, db, tt.create)
			}
			execTriggerSQL(t, db, "INSERT INTO items (id, note) VALUES (1, 'kept')")
			rebuilt := triggerSchema("sqlite", "", "", tt.timing, tt.event, tt.body)
			_, err := Push(t.Context(), db, Options{
				SchemaFS: triggerFS(rebuilt), DatabaseURL: dsn, AllowDestructive: true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("push error = %v, want %q", err, tt.want)
			}
			checkSQLiteInt(t, db, "SELECT count(*) FROM items WHERE note = 'kept'", 1)
			requireTriggerNoop(t, db, dsn, original)
		})
	}
}

func TestPushSQLiteRebuildWithoutSharedColumns(t *testing.T) {
	for _, replacement := range []string{`newValue String @default("new") @map("new_value")`, `newValue String @map("new_value")`} {
		db, dsn := openTestSQLite(t)
		schema := func(column string) string {
			return fmt.Sprintf("datasource db { provider = \"sqlite\" url = \"unused\" }\nmodel Item {\n  %s\n  @@map(\"items\")\n}", column)
		}
		pushTriggerSchema(t, db, dsn, schema(`oldValue Int @map("old_value")`), false)
		execTriggerSQL(t, db, "INSERT INTO items (old_value) VALUES (7), (8)")
		_, err := Push(t.Context(), db, Options{
			SchemaFS: triggerFS(schema(replacement)), DatabaseURL: dsn, AllowDestructive: true,
		})
		if strings.Contains(replacement, "@default") {
			if err != nil {
				t.Fatal(err)
			}
			checkSQLiteInt(t, db, "SELECT count(*) FROM items WHERE new_value = 'new'", 2)
		} else {
			if err == nil || !strings.Contains(err.Error(), "NOT NULL constraint failed") {
				t.Fatalf("push error = %v, want NOT NULL constraint failure", err)
			}
			checkSQLiteInt(t, db, "SELECT sum(old_value) FROM items", 15)
		}
	}
}

func TestSQLitePushLocksExistingMetadataTableForWriting(t *testing.T) {
	db, _ := openTestSQLite(t)
	if err := ensureSchemaPushesTable(t.Context(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockSQLitePush(t.Context(), tx); err != nil {
		t.Fatal(err)
	}

	_, err = db.ExecContext(t.Context(), "CREATE TABLE concurrent_writer (id INTEGER)")
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("concurrent writer returned %v, want a lock", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	execTriggerSQL(t, db, "CREATE TABLE concurrent_writer (id INTEGER)")
}

func TestPushSQLitePartialIndexPredicateSurvivesRebuild(t *testing.T) {
	db, dsn := openTestSQLite(t)
	schema := func(where, extra string) string {
		return fmt.Sprintf(`datasource db { provider = "sqlite" url = "unused" }
model Item {
  id Int @id
  label String
  %s
  @@map("items")
  @@index([label], name: "idx_label", where: %q)
}`, extra, where)
	}
	first := schema("label = 'a  b'", "note String?")
	pushTriggerSchema(t, db, dsn, first, false)
	requireTriggerNoop(t, db, dsn, first)
	execTriggerSQL(t, db, "INSERT INTO items (id, label, note) VALUES (1, 'a  b', 'note')")
	updated := schema("label = 'a b'", "note String?")
	pushTriggerSchema(t, db, dsn, updated, true)
	requireTriggerNoop(t, db, dsn, updated)
	checkPredicate := func() {
		t.Helper()
		var indexSQL string
		err := db.QueryRowContext(t.Context(), "SELECT sql FROM main.sqlite_schema WHERE name = 'idx_label'").Scan(&indexSQL)
		if err != nil || !strings.HasSuffix(indexSQL, "WHERE label = 'a b'") {
			t.Fatalf("index SQL = %q, err = %v", indexSQL, err)
		}
	}
	checkPredicate()
	rebuilt := schema("label = 'a b'", "")
	pushTriggerSchema(t, db, dsn, rebuilt, true)
	requireTriggerNoop(t, db, dsn, rebuilt)
	checkPredicate()
}

func sqliteParentSchema(extra string, foreignKey bool) string {
	var relation string
	if foreignKey {
		relation = "parent Parent @relation(fields: [parentId], references: [id], onDelete: Cascade)"
	}
	return fmt.Sprintf(`datasource db {
  provider = "sqlite"
  url = "unused"
}
model Parent {
  id Int @id @default(autoincrement())
  %s
  @@map("parents")
}
model Child {
  id Int @id
  parentId Int @map("parent_id")
  %s
  @@map("children")
}
`, extra, relation)
}

func checkSQLiteInt(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(t.Context(), query).Scan(&got); err != nil || got != want {
		t.Fatalf("%s returned %d, error = %v, want %d", query, got, err, want)
	}
}
