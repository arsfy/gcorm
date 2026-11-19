package migrate

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

func TestSQLiteTriggerMigrationRoundTrip(t *testing.T) {
	const base = `datasource db {
  provider = "sqlite"
  url = "local.db"
}
model Audit {
  item Int @id
  label String
  @@map("audit")
}
`
	schema := func(label string) string {
		src := base + "model Item {\n  id Int @id\n  @@map(\"items\")\n"
		if label != "" {
			src += `  @@trigger(name: "audit items", timing: After, event: Insert, body: "INSERT INTO audit VALUES (NEW.id, '` + label + `');")` + "\n"
		}
		return src + "}\n"
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	execSQL := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatalf("execute %q: %v", query, err)
		}
	}
	checkLabel := func(id int, want string) {
		t.Helper()
		var label string
		err := db.QueryRowContext(t.Context(), "SELECT label FROM audit WHERE item = ?", id).Scan(&label)
		if want == "" && err == sql.ErrNoRows {
			return
		}
		if err != nil || label != want {
			t.Fatalf("audit label for %d = %q, error = %v, want %q", id, label, err, want)
		}
	}

	schemaDir := writeTestSchema(t, schema("first"))
	migrations := t.TempDir()
	diff := func(name, label string) (up, down string, manifest MigrationManifest) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(schemaDir, "schema.gcorm"), []byte(schema(label)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runDiff([]string{"--name", name, "--dir", migrations, "--schema", schemaDir}); err != nil {
			t.Fatal(err)
		}
		dirs, err := filepath.Glob(filepath.Join(migrations, "*_"+name+"*"))
		if err != nil || len(dirs) != 1 {
			t.Fatalf("migration dirs = %v, error = %v", dirs, err)
		}
		read := func(file string) []byte {
			t.Helper()
			content, err := os.ReadFile(filepath.Join(dirs[0], file))
			if err != nil {
				t.Fatal(err)
			}
			return content
		}
		if err := json.Unmarshal(read("manifest.json"), &manifest); err != nil {
			t.Fatal(err)
		}
		return string(read("up.sql")), string(read("down.sql")), manifest
	}

	initial, teardown, initialManifest := diff("init", "first")
	if len(initialManifest.Changes) != 3 || initialManifest.Changes[2].Type != string(AddTrigger) {
		t.Fatalf("unexpected initial changes: %+v", initialManifest.Changes)
	}
	execSQL(initial)
	execSQL("INSERT INTO items VALUES (1)")
	checkLabel(1, "first")

	up, down, manifest := diff("replace", "second")
	if len(manifest.Changes) != 2 || manifest.Changes[0].Type != string(DropTrigger) ||
		manifest.Changes[1].Type != string(AddTrigger) || !manifest.ReviewRequired {
		t.Fatalf("unexpected replacement manifest: %+v", manifest)
	}
	execSQL(up)
	execSQL("INSERT INTO items VALUES (2)")
	checkLabel(2, "second")
	execSQL(down)
	execSQL("INSERT INTO items VALUES (3)")
	checkLabel(3, "first")
	execSQL(up)

	up, down, _ = diff("remove", "")
	execSQL(up)
	execSQL("INSERT INTO items VALUES (4)")
	checkLabel(4, "")
	execSQL(down)
	execSQL("INSERT INTO items VALUES (5)")
	checkLabel(5, "second")
	execSQL(teardown)
}

func TestTriggerMigrationDialectSQL(t *testing.T) {
	for _, tt := range []struct {
		dialect, name, body string
		up, down            []string
	}{
		{
			dialect: "postgresql", name: `mixed"Case`, body: "BEGIN\nRAISE NOTICE '$gco$';\nRETURN OLD;\nEND;",
			up:   []string{"CREATE OR REPLACE FUNCTION", "AS $gco1$", `CREATE TRIGGER "mixed""Case" BEFORE DELETE ON "items" FOR EACH ROW EXECUTE FUNCTION`},
			down: []string{`DROP TRIGGER IF EXISTS "mixed""Case" ON "items";`, "DROP FUNCTION IF EXISTS"},
		},
		{
			dialect: "mysql", name: "mixed`Case", body: "INSERT INTO audit VALUES ('//');\nSET @audited = OLD.id;",
			up:   []string{"DELIMITER ~gco1~", "CREATE TRIGGER `mixed``Case` BEFORE DELETE ON `items` FOR EACH ROW", "END~gco1~\nDELIMITER ;"},
			down: []string{"DROP TRIGGER IF EXISTS `mixed``Case`;"},
		},
	} {
		t.Run(tt.dialect, func(t *testing.T) {
			oldModel := testModel("items", testField("id", "Int"))
			newModel := *oldModel
			newModel.Triggers = []*ir.Trigger{{Name: tt.name, Timing: "BEFORE", Event: "DELETE", Body: tt.body}}
			newSchema := testSchema(&newModel)
			cs := Diff(testSchema(oldModel), newSchema)
			gen := DDLGenerator{Dialect: tt.dialect, Schema: newSchema}
			up, down := gen.GenerateUp(cs), gen.GenerateDown(cs)
			for _, want := range tt.up {
				if !strings.Contains(up, want) {
					t.Errorf("up SQL missing %q:\n%s", want, up)
				}
			}
			for _, want := range tt.down {
				if !strings.Contains(down, want) {
					t.Errorf("down SQL missing %q:\n%s", want, down)
				}
			}
		})
	}
}
