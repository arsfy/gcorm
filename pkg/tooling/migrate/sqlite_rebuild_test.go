package migrate

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/ir"
)

func TestSQLiteRebuildMigrationUpAndDown(t *testing.T) {
	oldSchema := compileSQLiteRebuildSchema(t, "label String?", "old", true)
	newSchema := compileSQLiteRebuildSchema(t, `label String @default("new")`, "changed", true)
	db := openSQLiteMigrationDB(t)
	applySQLiteMigration(t, db, (DDLGenerator{Dialect: "sqlite", Schema: oldSchema}).GenerateUp(Diff(nil, oldSchema)))
	applySQLiteMigration(t, db, `INSERT INTO parents (id, label) VALUES (1, 'original'), (50, 'retired');
DELETE FROM parents WHERE id = 50;
INSERT INTO children (id, parent_id) VALUES (1, 1);
CREATE VIEW parent_ids AS SELECT id FROM parents;`)

	gen := DDLGenerator{Dialect: "sqlite", Schema: newSchema}
	changes := Diff(oldSchema, newSchema)
	applySQLiteMigration(t, db, gen.GenerateUp(changes))
	applySQLiteMigration(t, db, "INSERT INTO parents DEFAULT VALUES;")
	checkSQLiteMigrationString(t, db, "SELECT marker FROM audit WHERE id = 51", "changed")
	checkSQLiteMigrationString(t, db, "SELECT label FROM parents WHERE id = 51", "new")
	checkSQLiteMigrationString(t, db, "SELECT count(*) FROM children WHERE parent_id = 1", "1")
	checkSQLiteMigrationString(t, db, "SELECT max(id) FROM parent_ids", "51")
	checkSQLiteMigrationString(t, db, "PRAGMA foreign_keys", "1")

	applySQLiteMigration(t, db, gen.GenerateDown(changes))
	applySQLiteMigration(t, db, "INSERT INTO parents DEFAULT VALUES;")
	checkSQLiteMigrationString(t, db, "SELECT marker FROM audit WHERE id = 52", "old")
	checkSQLiteMigrationString(t, db, "SELECT count(*) FROM parents WHERE id = 52 AND label IS NULL", "1")
	checkSQLiteMigrationString(t, db, "SELECT count(*) FROM children WHERE parent_id = 1", "1")
	checkSQLiteMigrationString(t, db, "SELECT max(id) FROM parent_ids", "52")
	checkSQLiteMigrationString(t, db, "PRAGMA foreign_keys", "1")
}

func TestSQLiteRebuildMigrationAbortsOnForeignKeyViolation(t *testing.T) {
	old := compileSQLiteRebuildSchema(t, "label String?", "old", false)
	newSchema := compileSQLiteRebuildSchema(t, "label String?", "old", true)
	db := openSQLiteMigrationDB(t)
	applySQLiteMigration(t, db, (DDLGenerator{Dialect: "sqlite", Schema: old}).GenerateUp(Diff(nil, old)))
	applySQLiteMigration(t, db, "INSERT INTO children VALUES (1, 999);")

	sqlText := (DDLGenerator{Dialect: "sqlite", Schema: newSchema}).GenerateUp(Diff(old, newSchema))
	if _, err := db.Exec(sqlText); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed: foreign_key_check") {
		t.Fatalf("rebuild returned %v, want foreign key failure", err)
	}
	checkSQLiteMigrationString(t, db, "SELECT parent_id FROM children WHERE id = 1", "999")
	checkSQLiteMigrationString(t, db, "SELECT count(*) FROM pragma_foreign_key_list('children')", "0")
	checkSQLiteMigrationString(t, db, "SELECT count(*) FROM main.sqlite_schema WHERE name = '__gco_new_children'", "0")
}

func compileSQLiteRebuildSchema(t *testing.T, field, marker string, hasForeignKey bool) *ir.Schema {
	t.Helper()
	var relation string
	if hasForeignKey {
		relation = "parent Parent @relation(fields: [parentId], references: [id], onDelete: Cascade)"
	}
	src := fmt.Sprintf(`datasource db { provider = "sqlite" url = "unused" }
model Parent {
  id Int @id @default(autoincrement())
  %s
  @@map("parents")
  @@trigger(name: "audit_parent", timing: After, event: Insert, body: "INSERT INTO audit VALUES (NEW.id, '%s');")
}
model Child {
  id Int @id
  parentId Int @map("parent_id")
  %s
  @@map("children")
}
model Audit {
  id Int @id
  marker String
  @@map("audit")
}`, field, marker, relation)
	result := compiler.CompileFile("schema.gcorm", []byte(src))
	if result.HasErrors() {
		t.Fatal(result.Errors)
	}
	return result.Schema
}

func openSQLiteMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	applySQLiteMigration(t, db, "PRAGMA foreign_keys = ON")
	return db
}

func applySQLiteMigration(t *testing.T, db *sql.DB, sqlText string) {
	t.Helper()
	if _, err := db.Exec(sqlText); err != nil {
		t.Fatalf("exec: %v\n%s", err, sqlText)
	}
}

func checkSQLiteMigrationString(t *testing.T, db *sql.DB, query, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
		t.Fatalf("%s returned %q, error = %v, want %q", query, got, err, want)
	}
}
