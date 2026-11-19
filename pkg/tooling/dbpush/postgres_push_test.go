package dbpush

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/schemautil"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgreSQL db push integration tests run when GCO_TEST_POSTGRES_URL is set.
// Each test uses a fresh base schema as search_path; {{b}} in schema text
// expands to it, so extra schemas are named {{b}}_<suffix>.

var postgresPushSeq atomic.Int64

type postgresPushDB struct {
	t    *testing.T
	db   *sql.DB
	dsn  string
	base string
}

func openPostgresPushDB(t *testing.T) *postgresPushDB {
	t.Helper()
	rawURL := os.Getenv("GCO_TEST_POSTGRES_URL")
	if rawURL == "" {
		t.Skip("set GCO_TEST_POSTGRES_URL to run PostgreSQL db push tests")
	}
	base := fmt.Sprintf("gco_push_%d_%d", time.Now().UnixNano(), postgresPushSeq.Add(1))
	admin, err := sql.Open("pgx", rawURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.Exec(`CREATE SCHEMA "` + base + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rows, err := admin.Query(`SELECT nspname FROM pg_namespace WHERE nspname = $1 OR starts_with(nspname, $1 || '_')`, base)
		if err != nil {
			t.Error(err)
			return
		}
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Error(err)
			}
			names = append(names, `"`+name+`"`)
		}
		rows.Close()
		if _, err := admin.Exec("DROP SCHEMA " + strings.Join(names, ", ") + " CASCADE"); err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", base)
	// Enum types are recreated; avoid cached statements on dropped types.
	q.Set("default_query_exec_mode", "exec")
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &postgresPushDB{t: t, db: db, dsn: u.String(), base: base}
}

func (p *postgresPushDB) content(body string) string {
	return `datasource db {
  provider = "postgresql"
  url      = "unused"
}
` + strings.ReplaceAll(body, "{{b}}", p.base)
}

func (p *postgresPushDB) push(body string, force bool) *Result {
	p.t.Helper()
	result, err := Push(p.t.Context(), p.db, Options{SchemaFS: triggerFS(p.content(body)), DatabaseURL: p.dsn, AllowDestructive: force})
	if err != nil {
		p.t.Fatalf("push: %v", err)
	}
	return result
}

// requireNoop pushes again and reports the planned changes if any remain.
func (p *postgresPushDB) requireNoop(body string) {
	p.t.Helper()
	result, err := Push(p.t.Context(), p.db, Options{SchemaFS: triggerFS(p.content(body)), DatabaseURL: p.dsn, DryRun: true, AllowDestructive: true})
	if err != nil {
		p.t.Fatalf("dry-run push: %v", err)
	}
	if !result.Noop {
		p.t.Fatalf("push did not converge; %d change(s):\n%s", result.ChangeCount, strings.Join(result.Statements, "\n"))
	}
	if result := p.push(body, false); !result.Noop {
		p.t.Fatalf("second push applied %v", result.Statements)
	}
}

func (p *postgresPushDB) exec(query string, args ...any) {
	p.t.Helper()
	if _, err := p.db.Exec(query, args...); err != nil {
		p.t.Fatalf("exec %q: %v", query, err)
	}
}

func (p *postgresPushDB) check(want, query string, args ...any) {
	p.t.Helper()
	var got sql.NullString
	if err := p.db.QueryRow(query, args...).Scan(&got); err != nil || got.String != want {
		p.t.Fatalf("%s = %q (error %v), want %q", query, got.String, err, want)
	}
}

func (p *postgresPushDB) introspect(target string) *ir.Schema {
	p.t.Helper()
	tx, err := p.db.Begin()
	if err != nil {
		p.t.Fatal(err)
	}
	defer tx.Rollback()
	schema, err := introspectPostgresWithTarget(p.t.Context(), tx, p.base, compileTestContent(p.t, p.content(target)))
	if err != nil {
		p.t.Fatal(err)
	}
	return schema
}

func compileTestContent(t *testing.T, content string) *ir.Schema {
	t.Helper()
	loaded, err := schemautil.LoadFS(triggerFS(content), "")
	if err != nil {
		t.Fatal(err)
	}
	return loaded.Schema
}

const comprehensivePushSchema = `
enum Role {
  ADMIN @map("admin")
  USER
  GUEST @map("guest")
}

model Account {
  id        String   @id @default(cuid())
  email     String   @unique
  role      Role     @default(ADMIN)
  roles     Role[]   @default([USER, GUEST])
  token     String   @default(dbgenerated("md5(random()::text)"))
  balance   Decimal  @default(0)
  ratio     Float
  born      DateTime @db.Date
  tags      String[]
  qty       Int      @db.Int
  opensAt   DateTime @db.Time
  code      String   @db.Char(10)
  label     String   @db.VarChar(40)
  shortName String   @db.VarChar
  extId     String   @db.Uuid
  createdAt DateTime @default(now()) @db.Timestamptz(3)
  thisIsAnExtremelyLongColumnNameForTestingIdentifierLimitsInIndexes String @map("this_is_an_extremely_long_column_name_for_identifier_limits") @default("")
  users     User[]

  @@index([balance])
  @@index([ratio])
  @@index([born])
  @@index([tags])
  @@index([label])
  @@index([thisIsAnExtremelyLongColumnNameForTestingIdentifierLimitsInIndexes, role])
  @@schema("{{b}}_crm")
}

model User {
  id        Int     @id @default(autoincrement())
  accountId String  @map("account_id")
  account   Account @relation(fields: [accountId], references: [id], onDelete: Cascade)
  name      String

  @@index([name])
  @@map("users")
  @@schema("{{b}}_crm")
}

model AuditUser {
  id    Int    @id @default(autoincrement())
  name  String
  ownerId Int  @map("owner_id")
  owner User   @relation(fields: [ownerId], references: [id])

  @@unique([name, ownerId])
  @@map("users")
  @@schema("{{b}}_audit")
}

model Plain {
  id   Int    @id
  note String @default("x")
}
`

func TestPostgresPushComprehensiveSchemaConverges(t *testing.T) {
	p := openPostgresPushDB(t)
	result := p.push(comprehensivePushSchema, false)
	if result.Noop || len(result.AppliedStatements) == 0 {
		t.Fatalf("initial push = %+v", result)
	}
	crm, audit := p.base+"_crm", p.base+"_audit"
	p.check("1", `SELECT count(*) FROM pg_tables WHERE schemaname = $1 AND tablename = 'users'`, crm)
	p.check("1", `SELECT count(*) FROM pg_tables WHERE schemaname = $1 AND tablename = 'users'`, audit)
	p.check(crm+`.users`, `SELECT confrelid::regclass::text FROM pg_constraint WHERE conrelid = $1::regclass AND contype = 'f'`, `"`+audit+`".users`)
	p.exec(`INSERT INTO "` + crm + `"."Account" (id, email, ratio, born, tags, qty, opens_at, code, label, short_name, ext_id)
VALUES ('a1', 'a@example.com', 0.5, '2024-01-01', '{x}', 1, '10:00', 'c', 'l', 's', gen_random_uuid())`)
	p.check("admin {USER,guest} 32", `SELECT role || ' ' || roles::text || ' ' || length(token) FROM "`+crm+`"."Account"`)
	p.check("numeric(65,30)", `SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = $1::regclass AND attname = 'balance'`, `"`+crm+`"."Account"`)
	longIndex := migrate.DefaultIndexName("Account", []string{"this_is_an_extremely_long_column_name_for_identifier_limits", "role"}, false)
	p.check("1", `SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexname = $2`, crm, longIndex)

	p.requireNoop(comprehensivePushSchema)

	// Introspection keeps both same-named tables apart.
	live := p.introspect(comprehensivePushSchema)
	var located []string
	for _, model := range live.Models {
		if model.TableName() == "users" {
			located = append(located, model.Schema+":"+model.Name)
		}
	}
	if strings.Join(located, ",") != audit+":"+audit+".users,"+crm+":"+crm+".users" {
		t.Fatalf("users models = %v", located)
	}
}

func TestPostgresPushEnumValueUsedAsDefault(t *testing.T) {
	p := openPostgresPushDB(t)
	p.push("enum Role {\n  USER\n}\nmodel Item {\n  id Int @id\n  role Role @default(USER)\n}\n", false)
	p.exec(`INSERT INTO "Item" (id) VALUES (1)`)
	next := "enum Role {\n  OWNER\n  USER\n  ADMIN @map(\"admin\")\n}\nmodel Item {\n  id Int @id\n  role Role @default(USER)\n  tier Role @default(ADMIN)\n}\n"

	plan, err := Push(t.Context(), p.db, Options{SchemaFS: triggerFS(p.content(next)), DatabaseURL: p.dsn, DryRun: true, AllowDestructive: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'OWNER' BEFORE 'USER'`,
		`ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'admin' AFTER 'USER'`,
		`ALTER TABLE "Item" ADD COLUMN "tier" "Role" NOT NULL DEFAULT 'admin'`,
	}
	if strings.Join(plan.Statements, "\n") != strings.Join(want, "\n") {
		t.Fatalf("dry-run statements:\n%s", strings.Join(plan.Statements, "\n"))
	}

	result := p.push(next, true)
	if result.Noop || strings.Join(result.AppliedStatements, "\n") != strings.Join(want, "\n") || result.ChangeCount != plan.ChangeCount {
		t.Fatalf("push result = %+v", result)
	}
	p.check("{OWNER,USER,admin}", `SELECT enum_range(NULL::"Role")::text`)
	p.check("USER admin", `SELECT role || ' ' || tier FROM "Item"`)
	p.requireNoop(next)
}

func TestPostgresPushKeepsForeignKeyReferencedUniqueIndex(t *testing.T) {
	p := openPostgresPushDB(t)
	body := `
model Country {
  id     Int       @id
  code   String
  cities City[]
  @@unique([code])
}
model City {
  id          Int     @id
  countryCode String  @map("country_code")
  country     Country @relation(fields: [countryCode], references: [code])
}
`
	p.exec(`CREATE TABLE "Country" (id integer PRIMARY KEY, code text NOT NULL)`)
	p.exec(`CREATE UNIQUE INDEX country_code_idx ON "Country" (code)`)
	p.exec(`CREATE TABLE "City" (id integer PRIMARY KEY, country_code text NOT NULL REFERENCES "Country" (code))`)
	// The index backs the foreign key, but it belongs to no constraint of
	// its own table, so introspection reports it. It satisfies the
	// declared unique constraint.
	live := p.introspect(body)
	for _, model := range live.Models {
		if model.TableName() == "Country" && (len(model.Indexes) != 0 || len(model.UniqueConstraints) != 1 || model.UniqueConstraints[0].Name != "country_code_idx") {
			t.Fatalf("Country indexes = %#v, unique constraints = %#v", model.Indexes, model.UniqueConstraints)
		}
	}
	p.requireNoop(body)
	p.check("1", `SELECT count(*) FROM pg_index WHERE indrelid = '"Country"'::regclass AND indisunique AND NOT indisprimary`)

	// Without a matching declaration the index is reported as an index.
	live = p.introspect("model Country {\n  id Int @id\n  code String\n}\n")
	if model := live.Models[1]; model.TableName() != "Country" || len(model.Indexes) != 1 || model.Indexes[0].Name != "country_code_idx" || !model.Indexes[0].IsUnique {
		t.Fatalf("Country = %#v", model)
	}
}

func TestPostgresPushIgnoresPartitions(t *testing.T) {
	p := openPostgresPushDB(t)
	p.exec(`CREATE TABLE events (id bigint NOT NULL, at date NOT NULL, kind text NOT NULL, PRIMARY KEY (id, at)) PARTITION BY RANGE (at)`)
	p.exec(`CREATE TABLE events_2024 PARTITION OF events FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')`)
	p.exec(`CREATE TABLE events_2025 PARTITION OF events FOR VALUES FROM ('2025-01-01') TO ('2026-01-01')`)
	p.exec(`CREATE INDEX events_kind_idx ON events (kind)`)
	p.exec(`CREATE TABLE kinds (name text PRIMARY KEY)`)
	p.exec(`ALTER TABLE events ADD CONSTRAINT events_kind_fkey FOREIGN KEY (kind) REFERENCES kinds (name)`)
	body := `
model Event {
  id   BigInt
  at   DateTime @db.Date
  kind String
  ref  Kind     @relation(fields: [kind], references: [name])
  @@id([id, at])
  @@index([kind], name: "events_kind_idx")
  @@map("events")
}
model Kind {
  name   String  @id
  events Event[]
  @@map("kinds")
}
`
	live := p.introspect(body)
	var tables []string
	for _, model := range live.Models {
		tables = append(tables, model.TableName())
		if model.TableName() == "events" && (len(model.Indexes) != 1 || len(model.Relations) != 1 || model.PrimaryKey == nil) {
			t.Fatalf("events model = %#v", model)
		}
	}
	if strings.Join(tables, ",") != "events,kinds" {
		t.Fatalf("introspected tables = %v", tables)
	}
	p.requireNoop(body)
}

func TestPostgresPushGeneratedColumnDoesNotChurn(t *testing.T) {
	p := openPostgresPushDB(t)
	base := "model Item {\n  id Int @id\n  price Int\n  %s\n}\n"
	p.push(fmt.Sprintf(base, ""), false)
	p.exec(`ALTER TABLE "Item" ADD COLUMN total integer GENERATED ALWAYS AS (price * 2) STORED`)
	p.exec(`ALTER TABLE "Item" ADD COLUMN seq integer GENERATED ALWAYS AS IDENTITY`)
	for _, field := range []string{
		"total Int? @default(dbgenerated(\"price * 2\"))\n  seq Int @default(autoincrement())",
		"total Int?\n  seq Int @default(autoincrement())",
		"total BigInt? @default(dbgenerated(\"price * 3\"))\n  seq Int @default(autoincrement())",
	} {
		p.requireNoop(fmt.Sprintf(base, field))
	}
	live := p.introspect(fmt.Sprintf(base, "total Int?"))
	total := findFieldByColumn(live.Models[0], "total")
	if total == nil || !total.IsGenerated || total.Default == nil || total.Default.FuncName != "dbgenerated" || total.Default.FuncArgs[0] != "(price * 2)" {
		t.Fatalf("total = %#v", total)
	}
	p.exec(`INSERT INTO "Item" (id, price) VALUES (1, 4)`)
	p.check("8 1", `SELECT total || ' ' || seq FROM "Item"`)
}

func TestPostgresPushMatchesTruncatedLegacyNames(t *testing.T) {
	p := openPostgresPushDB(t)
	column := "a_column_name_long_enough_to_overflow_index_names_xxxxx"
	body := "model Item {\n  id Int @id\n  value String @map(\"" + column + "\")\n  @@index([value])\n}\n"
	// Older versions created the index under the full name, which
	// PostgreSQL truncated to 63 bytes.
	p.exec(`CREATE TABLE "Item" (id integer PRIMARY KEY, "` + column + `" text NOT NULL)`)
	p.exec(`CREATE INDEX "idx_Item_` + column + `" ON "Item" ("` + column + `")`)
	p.requireNoop(body)
}
