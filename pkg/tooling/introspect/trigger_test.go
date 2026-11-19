package introspect

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/tooling/dbpush"
)

func TestIntrospectManagedSQLiteTrigger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const source = `datasource db {
  provider = "sqlite"
  url = "unused"
}
model Item {
  id Int @id
  @@map("items")
  @@trigger(name: "quoted \"trigger\"", timing: After, event: Delete, body: "SELECT 'a;\\b';\nSELECT OLD.id;")
}`
	_, err = dbpush.Push(t.Context(), db, dbpush.Options{
		SchemaFS:    fstest.MapFS{"schema.gcorm": {Data: []byte(source)}},
		DatabaseURL: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TRIGGER external_trigger AFTER INSERT ON items BEGIN SELECT 1; END"); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := Run([]string{"--provider", "sqlite", "--url", path, "--output", output}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(output, "schema.gcorm"))
	if err != nil {
		t.Fatal(err)
	}
	compiled := compiler.CompileFile("schema.gcorm", raw)
	if compiled.HasErrors() {
		t.Fatal(compiled.Errors)
	}
	triggers := compiled.Schema.Models[0].Triggers
	if len(triggers) != 1 || triggers[0].Name != `quoted "trigger"` || triggers[0].Timing != "AFTER" ||
		triggers[0].Event != "DELETE" || triggers[0].Body != "SELECT 'a;\\b';\nSELECT OLD.id;" {
		t.Fatalf("unexpected introspection result:\n%s", raw)
	}
	if strings.Contains(string(raw), "external_trigger") {
		t.Fatalf("introspection exported an external trigger:\n%s", raw)
	}
}

func TestIntrospectTriggerUpdateOfColumnsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const source = `datasource db {
  provider = "sqlite"
  url = "unused"
}
model Item {
  id         Int    @id
  priceCents Int    @map("price_cents")
  status     String
  @@map("items")
  @@trigger(name: "price_audit", timing: After, event: Update, columns: [priceCents, status], body: "SELECT NEW.id;")
}`
	if _, err := dbpush.Push(t.Context(), db, dbpush.Options{
		SchemaFS:    fstest.MapFS{"schema.gcorm": {Data: []byte(source)}},
		DatabaseURL: path,
	}); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := Run([]string{"--provider", "sqlite", "--url", path, "--output", output}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(output, "schema.gcorm"))
	if err != nil {
		t.Fatal(err)
	}
	compiled := compiler.CompileFile("schema.gcorm", raw)
	if compiled.HasErrors() {
		t.Fatalf("%v\n%s", compiled.Errors, raw)
	}
	if triggers := compiled.Schema.Models[0].Triggers; len(triggers) != 1 || len(triggers[0].Columns) != 2 {
		t.Fatalf("UPDATE OF columns not introspected:\n%s", raw)
	}

	// Pushing the introspected schema back is a no-op.
	result, err := dbpush.Push(t.Context(), db, dbpush.Options{
		SchemaFS:    fstest.MapFS{"schema.gcorm": {Data: raw}},
		DatabaseURL: path,
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Noop {
		t.Fatalf("introspected schema does not round-trip: %v\n%s", result.Statements, raw)
	}
}
