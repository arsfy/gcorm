package dbpush

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
)

func TestPostgresIntrospectionSchemasManagesDefaultOnlyWhenUsed(t *testing.T) {
	ds := func(schemas ...string) *ir.Datasource {
		return &ir.Datasource{Provider: "postgresql", Schemas: schemas}
	}
	tests := []struct {
		name   string
		target *ir.Schema
		want   []string
	}{
		{"no target", nil, []string{"public"}},
		{"single schema mode", &ir.Schema{Datasource: &ir.Datasource{Provider: "postgresql"}, Models: []*ir.Model{{Name: "A", Schema: "app"}}}, []string{"public", "app"}},
		{"schemas list without default", &ir.Schema{Datasource: ds("billing", "app"), Models: []*ir.Model{{Name: "A", Schema: "app"}, {Name: "B", Schema: "billing"}}}, []string{"app", "billing"}},
		{"schemas list naming default", &ir.Schema{Datasource: ds("app", "public"), Models: []*ir.Model{{Name: "A", Schema: "app"}}}, []string{"public", "app"}},
		{"model without @@schema", &ir.Schema{Datasource: ds("app", "billing"), Models: []*ir.Model{{Name: "A", Schema: "app"}, {Name: "B"}}}, []string{"public", "app", "billing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresIntrospectionSchemas(tt.target, "public"); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("postgresIntrospectionSchemas() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A push whose default schema does not exist yet must create it rather than
// let PostgreSQL fall through search_path into public.
func TestPostgresPushCreatesMissingDefaultSchema(t *testing.T) {
	p := openPostgresPushDB(t)
	missing := p.base + "_fresh"
	u, err := url.Parse(p.dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", missing)
	u.RawQuery = q.Encode()

	content := p.content("model Widget {\n  id Int @id\n\n  @@map(\"widgets\")\n}\n")
	if _, err := Push(t.Context(), p.db, Options{SchemaFS: triggerFS(content), DatabaseURL: u.String()}); err != nil {
		t.Fatalf("push: %v", err)
	}
	p.check("1", `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'widgets'`, missing)
	p.check("0", `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'widgets'`)
}

// serial columns introspect as autoincrement but are not identity columns;
// dropping autoincrement must remove the nextval() default and converge.
func TestPostgresPushDropsSerialDefault(t *testing.T) {
	p := openPostgresPushDB(t)
	p.exec(`CREATE TABLE "` + p.base + `"."counters" (id serial PRIMARY KEY, n integer)`)
	body := "model Counter {\n  id Int @id\n  n  Int?\n\n  @@map(\"counters\")\n}\n"
	p.push(body, true)
	p.check("", `SELECT column_default FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'counters' AND column_name = 'id'`, p.base)
	p.requireNoop(body)
}

// With a datasource schemas list that does not include the default schema
// and no model living there, unrelated tables in the default schema are not
// managed (never planned as drops, even with --force).
func TestPostgresPushSchemasListLeavesUnlistedDefaultAlone(t *testing.T) {
	p := openPostgresPushDB(t)
	p.exec(`CREATE TABLE "` + p.base + `"."legacy" (id integer PRIMARY KEY)`)
	content := strings.ReplaceAll(`datasource db {
  provider = "postgresql"
  url      = "unused"
  schemas  = ["{{b}}_app", "{{b}}_billing"]
}

model Account {
  id Int @id

  @@schema("{{b}}_app")
  @@map("accounts")
}

model Invoice {
  id Int @id

  @@schema("{{b}}_billing")
  @@map("invoices")
}
`, "{{b}}", p.base)
	result, err := Push(t.Context(), p.db, Options{SchemaFS: triggerFS(content), DatabaseURL: p.dsn, AllowDestructive: true})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	for _, stmt := range result.Statements {
		if strings.Contains(stmt, "legacy") {
			t.Fatalf("push touched the unmanaged default schema: %s", stmt)
		}
	}
	p.check("legacy", `SELECT table_name FROM information_schema.tables WHERE table_schema = $1`, p.base)
	p.check("1", `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'accounts'`, p.base+"_app")
}
