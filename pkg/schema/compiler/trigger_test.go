package compiler

import (
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/formatter"
	"github.com/arsfy/gcorm/pkg/schema/parser"
)

func TestCompileAndFormatTrigger(t *testing.T) {
	src := `model Item {
  id Int @id
  @@trigger(
    name: "items_audit",
    timing: aFtEr,
    event: Update,
    body: "INSERT INTO \"entries\" VALUES (NEW.id, 'a;  b');\n-- SQL comment"
  )
}`
	doc, err := parser.Parse("trigger.gcorm", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	formatted := formatter.Format(doc)
	for _, source := range []string{src, formatted} {
		compiled := CompileFile("trigger.gcorm", []byte(source))
		if compiled.HasErrors() {
			t.Fatal(compiled.Errors)
		}
		triggers := compiled.Schema.Models[0].Triggers
		if len(triggers) != 1 {
			t.Fatalf("triggers = %v, want one trigger", triggers)
		}
		trigger := triggers[0]
		if trigger.Name != "items_audit" || trigger.Timing != "AFTER" || trigger.Event != "UPDATE" ||
			trigger.Body != "INSERT INTO \"entries\" VALUES (NEW.id, 'a;  b');\n-- SQL comment" {
			t.Fatalf("unexpected trigger: %+v", trigger)
		}
	}
}

func TestCompileRejectsInvalidTrigger(t *testing.T) {
	for _, tt := range []struct {
		name, args, want string
	}{
		{"missing body", `name: "audit", timing: Before, event: Delete`, `requires the "body" argument`},
		{"duplicate arg", `name: "audit", name: "second"`, `repeats argument "name"`},
		{"positional", `"audit", timing: After`, "requires named arguments"},
		{"unknown arg", `when: "true"`, `unknown @@trigger argument "when"`},
		{"timing", `timing: InsteadOf`, "timing must be Before or After"},
		{"event", `event: Truncate`, "event must be Insert, Update, or Delete"},
		{"body type", `body: env("SQL")`, "body must be a non-empty SQL string"},
		{"blank body", `body: " \n"`, "body must be a non-empty SQL string"},
		{"blank name", `name: ""`, "valid, non-empty database identifier"},
		{"long name", `name: "` + strings.Repeat("审", 22) + `"`, "valid, non-empty database identifier"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := "model Item {\n  id Int @id\n  @@trigger(" + tt.args + ")\n}"
			result := CompileFile("invalid.gcorm", []byte(src))
			for _, err := range result.Errors {
				if strings.Contains(err.Error(), "invalid.gcorm:3:") && strings.Contains(err.Error(), tt.want) {
					return
				}
			}
			t.Fatalf("errors = %v, want %q on line 3", result.Errors, tt.want)
		})
	}
}

func TestTriggerNamesAcrossSchemaFiles(t *testing.T) {
	const trigger = `@@trigger(name: "audit", timing: After, event: Insert, body: "SELECT 1;")`
	for _, provider := range []string{"sqlite", "mysql", "postgresql"} {
		t.Run(provider, func(t *testing.T) {
			files := map[string][]byte{
				"db.gcorm":    []byte("datasource db { provider = \"" + provider + "\" url = \"local\" }"),
				"items.gcorm": []byte("model Item {\n id Int @id\n " + trigger + "\n}"),
				"logs.gcorm":  []byte("model Log {\n id Int @id\n " + trigger + "\n}"),
			}
			docs, err := parser.ParseMulti(files)
			if err != nil {
				t.Fatal(err)
			}
			result := Compile(docs)
			if provider == "postgresql" {
				if result.HasErrors() {
					t.Fatal(result.Errors)
				}
			} else if !result.HasErrors() || !strings.Contains(result.Errors[0].Error(), `duplicate trigger name "audit"`) {
				t.Fatalf("errors = %v, want duplicate trigger name error", result.Errors)
			}
			files["logs.gcorm"] = []byte("model Log {\n id Int @id\n " + trigger + "\n " + trigger + "\n}")
			docs, err = parser.ParseMulti(files)
			if err != nil {
				t.Fatal(err)
			}
			result = Compile(docs)
			if !result.HasErrors() || !strings.Contains(result.Errors[0].Error(), "duplicate trigger name") {
				t.Fatalf("errors = %v, want duplicate trigger name in the same model", result.Errors)
			}
		})
	}
}

func TestCompileTriggerUpdateOfColumns(t *testing.T) {
	src := `datasource db {
  provider = "postgresql"
  url      = "local"
}
model Item {
  id         Int  @id
  priceCents Int  @map("price_cents")
  status     String
  @@trigger(name: "price_audit", timing: After, event: Update, columns: [priceCents, status], body: "BEGIN RETURN NEW; END;")
}`
	doc, err := parser.Parse("columns.gcorm", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{src, formatter.Format(doc)} {
		compiled := CompileFile("columns.gcorm", []byte(source))
		if compiled.HasErrors() {
			t.Fatal(compiled.Errors)
		}
		trigger := compiled.Schema.Models[0].Triggers[0]
		if strings.Join(trigger.Columns, ",") != "priceCents,status" {
			t.Fatalf("trigger columns = %v", trigger.Columns)
		}
	}
}

func TestCompileRejectsInvalidTriggerColumns(t *testing.T) {
	const fields = "model Item {\n  id Int @id\n  note String?\n  owner User @relation(fields: [ownerId], references: [id])\n  ownerId Int\n"
	const user = "model User {\n  id Int @id\n  items Item[]\n}\n"
	for _, tt := range []struct {
		name, provider, args, want string
	}{
		{"insert event", "postgresql", `event: Insert, columns: [note]`, "columns requires event: Update"},
		{"mysql", "mysql", `event: Update, columns: [note]`, "not supported by MySQL"},
		{"unknown field", "sqlite", `event: Update, columns: [missing]`, `unknown field "missing"`},
		{"relation field", "sqlite", `event: Update, columns: [owner]`, `relation field "owner"`},
		{"duplicate", "sqlite", `event: Update, columns: [note, note]`, `repeats field "note"`},
		{"empty", "postgresql", `event: Update, columns: []`, "non-empty array"},
		{"not array", "postgresql", `event: Update, columns: note`, "non-empty array"},
		{"string element", "postgresql", `event: Update, columns: ["note"]`, "must be field names"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := "datasource db {\n  provider = \"" + tt.provider + "\"\n  url = \"local\"\n}\n" + user + fields +
				`  @@trigger(name: "audit", timing: After, ` + tt.args + `, body: "SELECT 1;")` + "\n}\n"
			result := CompileFile("invalid.gcorm", []byte(src))
			for _, err := range result.Errors {
				if strings.Contains(err.Error(), tt.want) {
					return
				}
			}
			t.Fatalf("errors = %v, want %q", result.Errors, tt.want)
		})
	}
}
