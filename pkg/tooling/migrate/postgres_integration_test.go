package migrate

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/triggerddl"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgreSQL migration integration tests run when GCO_TEST_POSTGRES_URL is
// set. Each test works in fresh schemas named after a unique base schema,
// which is also the connection's search_path.

var postgresMigrationSeq atomic.Int64

type postgresMigrationDB struct {
	t    *testing.T
	db   *sql.DB
	base string
}

func openPostgresMigrationDB(t *testing.T) *postgresMigrationDB {
	t.Helper()
	rawURL := os.Getenv("GCO_TEST_POSTGRES_URL")
	if rawURL == "" {
		t.Skip("set GCO_TEST_POSTGRES_URL to run PostgreSQL migration tests")
	}
	admin, err := sql.Open("pgx", rawURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	base := fmt.Sprintf("gco_mig_%d_%d", time.Now().UnixNano(), postgresMigrationSeq.Add(1))
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
		if len(names) > 0 {
			if _, err := admin.Exec("DROP SCHEMA " + strings.Join(names, ", ") + " CASCADE"); err != nil {
				t.Error(err)
			}
		}
	})
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", base)
	// Enum types are recreated by the tests; avoid cached statements that
	// reference dropped types.
	q.Set("default_query_exec_mode", "exec")
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &postgresMigrationDB{t: t, db: db, base: base}
}

// schema compiles a PostgreSQL schema; {{b}} expands to the base schema.
func (p *postgresMigrationDB) schema(body string) *ir.Schema {
	p.t.Helper()
	return compileMigrateSchema(p.t, postgresTestDatasource+strings.ReplaceAll(body, "{{b}}", p.base))
}

// migrate applies the up migration from old to new like a driver: the
// RequiresCommit steps commit in their own transaction before the rest.
func (p *postgresMigrationDB) migrate(old, new *ir.Schema) *Changeset {
	p.t.Helper()
	cs := Diff(old, new)
	var commit, rest []DDLStep
	for _, step := range (DDLGenerator{Dialect: "postgresql", Schema: new}).UpSteps(cs) {
		if step.RequiresCommit {
			if len(rest) > 0 {
				p.t.Fatalf("RequiresCommit step after other steps: %q", step.SQL)
			}
			commit = append(commit, step)
		} else {
			rest = append(rest, step)
		}
	}
	p.applyInTransaction(commit)
	p.applyInTransaction(rest)
	return cs
}

func (p *postgresMigrationDB) applyInTransaction(steps []DDLStep) {
	p.t.Helper()
	if len(steps) == 0 {
		return
	}
	tx, err := p.db.Begin()
	if err != nil {
		p.t.Fatal(err)
	}
	defer tx.Rollback()
	for _, step := range steps {
		if _, err := tx.Exec(step.SQL); err != nil {
			p.t.Fatalf("exec step: %v\n%s", err, step.SQL)
		}
	}
	if err := tx.Commit(); err != nil {
		p.t.Fatal(err)
	}
}

// down applies the down script of the migration from old to new.
func (p *postgresMigrationDB) down(old, new *ir.Schema) {
	p.t.Helper()
	p.exec(DDLGenerator{Dialect: "postgresql", Schema: new}.GenerateDown(Diff(old, new)))
}

func (p *postgresMigrationDB) exec(sqlText string, args ...any) {
	p.t.Helper()
	if _, err := p.db.Exec(sqlText, args...); err != nil {
		p.t.Fatalf("exec: %v\n%s", err, sqlText)
	}
}

func (p *postgresMigrationDB) expectError(code, sqlText string, args ...any) {
	p.t.Helper()
	_, err := p.db.Exec(sqlText, args...)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		p.t.Fatalf("%s returned %v, want SQLSTATE %s", sqlText, err, code)
	}
}

func (p *postgresMigrationDB) query(sqlText string, args ...any) string {
	p.t.Helper()
	var got sql.NullString
	if err := p.db.QueryRow(sqlText, args...).Scan(&got); err != nil {
		p.t.Fatalf("query: %v\n%s", err, sqlText)
	}
	return got.String
}

func (p *postgresMigrationDB) check(want, sqlText string, args ...any) {
	p.t.Helper()
	if got := p.query(sqlText, args...); got != want {
		p.t.Fatalf("%s = %q, want %q", sqlText, got, want)
	}
}

// columnType returns the formatted type and default of a column.
func (p *postgresMigrationDB) column(schemaName, table, column string) (string, string) {
	p.t.Helper()
	var typ string
	var def sql.NullString
	err := p.db.QueryRow(`
SELECT format_type(a.atttypid, a.atttypmod), pg_get_expr(d.adbin, d.adrelid)
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE n.nspname = $1 AND c.relname = $2 AND a.attname = $3`, schemaName, table, column).Scan(&typ, &def)
	if err != nil {
		p.t.Fatalf("column %s.%s.%s: %v", schemaName, table, column, err)
	}
	return typ, def.String
}

func TestPostgresMigrationInit(t *testing.T) {
	p := openPostgresMigrationDB(t)
	longColumn := "this_is_a_very_long_column_name_used_for_identifier_limit_tests"
	longTable := "a_table_name_that_is_long_enough_to_need_truncation_by_postgresql"
	schema := p.schema(`
enum Role {
  ADMIN @map("admin")
  USER
}
model Account {
  id    String @id @default(cuid())
  email String @unique
  items Item[]
  @@schema("{{b}}_crm")
}
model Item {
  id        Int      @id @default(autoincrement())
  accountId String   @map("account_id")
  account   Account  @relation(fields: [accountId], references: [id])
  roles     Role[]   @default([ADMIN])
  role      Role     @default(ADMIN)
  token     String   @default(dbgenerated("md5(concat(random()::text, 'x', clock_timestamp()::text))"))
  tags      String[] @default([])
  ids       UUID[]   @default(["7d9f1c2e-0000-4000-8000-000000000000"])
  codes     String[] @db.VarChar(10)
  longValue String   @map("` + longColumn + `") @default("x")
  @@index([longValue, role])
  @@unique([accountId, longValue])
  @@schema("{{b}}_audit")
  @@trigger(name: "item_upper", timing: Before, event: Insert, body: "BEGIN\n  NEW.token := upper(NEW.token);\n  RETURN NEW;\nEND;")
}
model LongTable {
  id Int @id
  a  Int
  b  Int
  @@map("` + longTable + `")
}
`)
	p.migrate(nil, schema)
	audit, crm := p.base+"_audit", p.base+"_crm"

	// cuid() has no database default; the client supplies the value.
	if _, def := p.column(crm, "Account", "id"); def != "" {
		t.Fatalf("cuid column default = %q, want none", def)
	}
	p.exec(`INSERT INTO "` + crm + `"."Account" (id, email) VALUES ('c1', 'a@example.com')`)
	p.exec(`INSERT INTO "` + audit + `"."Item" (account_id, codes) VALUES ('c1', '{x}')`)
	p.check("1|admin|{admin}|{}|{7d9f1c2e-0000-4000-8000-000000000000}|x|t",
		`SELECT concat_ws('|', id, role, roles, tags, ids, "`+longColumn+`", token = upper(token) AND length(token) = 32) FROM "`+audit+`"."Item"`)
	if typ, _ := p.column(audit, "Item", "roles"); typ != `"Role"[]` && typ != `"`+p.base+`"."Role"[]` {
		t.Fatalf("roles type = %q", typ)
	}
	if typ, _ := p.column(audit, "Item", "codes"); typ != "character varying(10)[]" {
		t.Fatalf("codes type = %q", typ)
	}

	// Generated names fit the identifier limit, so PostgreSQL keeps them.
	idx := LimitIdentifier("idx_Item_" + longColumn + "_role")
	uq := LimitIdentifier("uq_Item_account_id_" + longColumn)
	p.check("1", `SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexname = $2`, audit, idx)
	p.check("1", `SELECT count(*) FROM pg_constraint WHERE conname = $1 AND contype = 'u'`, uq)
	p.check(crm+`."Account"`, `SELECT confrelid::regclass::text FROM pg_constraint WHERE conname = 'fk_Item_account_id'`)
	p.check("1", `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1 AND p.proname = $2`,
		audit, triggerddl.PostgresFunctionName("Item", "item_upper"))

	// PostgreSQL-assigned names match the helpers used to drop them.
	p.check(PostgresPrimaryKeyName(longTable), `SELECT conname FROM pg_constraint WHERE conrelid = $1::regclass AND contype = 'p'`, `"`+longTable+`"`)
	p.exec(`ALTER TABLE "` + longTable + `" ADD UNIQUE (a, b)`)
	p.check(PostgresUniqueKeyName(longTable, []string{"a", "b"}), `SELECT conname FROM pg_constraint WHERE conrelid = $1::regclass AND contype = 'u'`, `"`+longTable+`"`)
	p.exec(DDLGenerator{Dialect: "postgresql"}.dropUniqueSQL(Change{Model: longTable, Details: map[string]string{"fields": "a,b"}}))
	p.check("0", `SELECT count(*) FROM pg_constraint WHERE conrelid = $1::regclass AND contype = 'u'`, `"`+longTable+`"`)

	p.down(nil, schema)
	p.check("0", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname LIKE $1 || '%' AND c.relkind = 'r'`, p.base)
	p.check("0", `SELECT count(*) FROM pg_type WHERE typname = 'Role' AND typnamespace = $1::regnamespace`, `"`+p.base+`"`)
}

func TestPostgresMigrationColumnTypeChanges(t *testing.T) {
	src := func(fields string) string {
		return `
enum Status {
  ACTIVE @map("active")
  INACTIVE @map("inactive")
}
model Item {
  id Int @id
  ` + fields + `
  @@schema("{{b}}_audit")
}
`
	}
	t.Run("string to enum with default", func(t *testing.T) {
		p := openPostgresMigrationDB(t)
		old := p.schema(src(`status String @default("active")`))
		next := p.schema(src(`status Status @default(ACTIVE)`))
		table := `"` + p.base + `_audit"."Item"`
		p.migrate(nil, old)
		p.exec(`INSERT INTO ` + table + ` (id, status) VALUES (1, 'inactive')`)
		p.migrate(old, next)
		p.exec(`INSERT INTO ` + table + ` (id) VALUES (2)`)
		p.check("inactive,active", `SELECT string_agg(status::text, ',' ORDER BY id) FROM `+table)
		if _, def := p.column(p.base+"_audit", "Item", "status"); !strings.HasPrefix(def, "'active'::") {
			t.Fatalf("status default = %q", def)
		}
		p.down(old, next)
		typ, def := p.column(p.base+"_audit", "Item", "status")
		if typ != "text" || def != "'active'::text" {
			t.Fatalf("reverted status column = %q default %q", typ, def)
		}
	})
	t.Run("string to int", func(t *testing.T) {
		p := openPostgresMigrationDB(t)
		old := p.schema(src(`code String @default("0")`))
		next := p.schema(src(`code Int @default(0)`))
		table := `"` + p.base + `_audit"."Item"`
		p.migrate(nil, old)
		p.exec(`INSERT INTO ` + table + ` (id, code) VALUES (1, '42')`)
		p.migrate(old, next)
		p.exec(`INSERT INTO ` + table + ` (id) VALUES (2)`)
		p.check("42", `SELECT sum(code)::text FROM `+table)
		p.down(old, next)
		p.check("text 42,0", `SELECT pg_typeof(code)::text || ' ' || string_agg(code, ',' ORDER BY id) FROM `+table+` GROUP BY pg_typeof(code)`)
	})
}

func TestPostgresMigrationEnumChanges(t *testing.T) {
	t.Run("added value used as a new column default", func(t *testing.T) {
		p := openPostgresMigrationDB(t)
		old := p.schema(`
enum Role {
  USER
}
model Item {
  id   Int  @id
  role Role @default(USER)
}
`)
		next := p.schema(`
enum Role {
  OWNER
  USER
  ADMIN @map("admin")
}
model Item {
  id   Int  @id
  role Role @default(USER)
  tier Role @default(ADMIN)
}
`)
		p.migrate(nil, old)
		p.exec(`INSERT INTO "Item" (id) VALUES (1)`)

		// Applying everything in one transaction uses an uncommitted label.
		tx, err := p.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var failure error
		for _, step := range (DDLGenerator{Dialect: "postgresql", Schema: next}).UpSteps(Diff(old, next)) {
			if _, failure = tx.Exec(step.SQL); failure != nil {
				break
			}
		}
		tx.Rollback()
		var pgErr *pgconn.PgError
		if !errors.As(failure, &pgErr) || pgErr.Code != "55P04" {
			t.Fatalf("single-transaction apply returned %v, want SQLSTATE 55P04", failure)
		}

		p.migrate(old, next)
		p.check("{OWNER,USER,admin}", `SELECT enum_range(NULL::"Role")::text`)
		p.check("USER admin", `SELECT role || ' ' || tier FROM "Item" WHERE id = 1`)

		p.down(old, next)
		p.check("{USER}", `SELECT enum_range(NULL::"Role")::text`)
		p.check("USER", `SELECT role::text FROM "Item" WHERE id = 1`)
	})

	t.Run("value rename and removal", func(t *testing.T) {
		p := openPostgresMigrationDB(t)
		src := func(values string) string {
			return `
enum Role {
` + values + `
}
model Item {
  id    Int    @id
  role  Role   @default(ADMIN)
  roles Role[] @default([USER])
  @@schema("{{b}}_audit")
}
`
		}
		old := p.schema(src("ADMIN @map(\"admin\")\nUSER\nLEGACY"))
		next := p.schema(src("ADMIN @map(\"adm\")\nUSER"))
		table := `"` + p.base + `_audit"."Item"`
		p.migrate(nil, old)
		p.exec(`INSERT INTO ` + table + ` (id, roles) VALUES (1, '{admin,USER}'), (2, DEFAULT)`)
		p.migrate(old, next)
		p.check("{adm,USER}", `SELECT enum_range(NULL::"Role")::text`)
		p.exec(`INSERT INTO ` + table + ` (id) VALUES (3)`)
		p.check("adm {adm,USER};adm {USER};adm {USER}", `SELECT string_agg(role || ' ' || roles::text, ';' ORDER BY id) FROM `+table)
		p.check("0", `SELECT count(*) FROM pg_type WHERE typname LIKE '%gco_old%'`)

		p.down(old, next)
		p.check("{admin,USER,LEGACY}", `SELECT enum_range(NULL::"Role")::text`)
		p.check("admin", `SELECT role::text FROM `+table+` WHERE id = 3`)
		p.exec(`INSERT INTO ` + table + ` (id, role) VALUES (4, 'LEGACY')`)
	})

	t.Run("value removal fails while rows use it", func(t *testing.T) {
		p := openPostgresMigrationDB(t)
		old := p.schema("enum Role {\n  USER\n  LEGACY\n}\nmodel Item {\n  id Int @id\n  role Role\n}\n")
		next := p.schema("enum Role {\n  USER\n}\nmodel Item {\n  id Int @id\n  role Role\n}\n")
		p.migrate(nil, old)
		p.exec(`INSERT INTO "Item" VALUES (1, 'LEGACY')`)
		steps := DDLGenerator{Dialect: "postgresql", Schema: next}.UpSteps(Diff(old, next))
		tx, err := p.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(steps[0].SQL); err == nil {
			t.Fatal("recreating the enum must fail for rows holding a removed value")
		}
	})

	t.Run("enum map renames the type", func(t *testing.T) {
		p := openPostgresMigrationDB(t)
		old := p.schema("enum Role {\n  USER\n}\nmodel Item {\n  id Int @id\n  role Role @default(USER)\n}\n")
		next := p.schema("enum Role {\n  USER\n  ADMIN\n  @@map(\"role_kind\")\n}\nmodel Item {\n  id Int @id\n  role Role @default(USER)\n}\n")
		p.migrate(nil, old)
		p.migrate(old, next)
		if typ, _ := p.column(p.base, "Item", "role"); typ != "role_kind" {
			t.Fatalf("role type = %q", typ)
		}
		p.check("{USER,ADMIN}", `SELECT enum_range(NULL::role_kind)::text`)
		p.down(old, next)
		if typ, _ := p.column(p.base, "Item", "role"); typ != `"Role"` {
			t.Fatalf("reverted role type = %q", typ)
		}
		p.check("{USER}", `SELECT enum_range(NULL::"Role")::text`)
	})
}

func TestPostgresMigrationIdentityChanges(t *testing.T) {
	src := func(field string) string {
		return "model Item {\n  id Int @id\n  " + field + "\n  @@schema(\"{{b}}_audit\")\n}\n"
	}
	t.Run("identity dropped and restored", func(t *testing.T) {
		p := openPostgresMigrationDB(t)
		identity := p.schema(src("n Int @default(autoincrement())"))
		plain := p.schema(src("n Int"))
		literal := p.schema(src("n Int @default(7)"))
		table := `"` + p.base + `_audit"."Item"`
		p.migrate(nil, identity)
		p.exec(`INSERT INTO ` + table + ` (id) VALUES (1), (2)`)

		p.migrate(identity, plain)
		p.check("", `SELECT attidentity::text FROM pg_attribute WHERE attrelid = $1::regclass AND attname = 'n'`, table)
		p.exec(`INSERT INTO ` + table + ` (id, n) VALUES (3, 40)`)

		// Restoring the identity continues after the largest value.
		p.down(identity, plain)
		p.exec(`INSERT INTO ` + table + ` (id) VALUES (4)`)
		p.check("41", `SELECT n::text FROM `+table+` WHERE id = 4`)

		p.migrate(identity, literal)
		p.exec(`INSERT INTO ` + table + ` (id) VALUES (5)`)
		p.check("7", `SELECT n::text FROM `+table+` WHERE id = 5`)

		p.migrate(literal, identity)
		p.exec(`INSERT INTO ` + table + ` (id) VALUES (6)`)
		p.check("42", `SELECT n::text FROM `+table+` WHERE id = 6`)
	})
	t.Run("identity added to an empty table", func(t *testing.T) {
		p := openPostgresMigrationDB(t)
		literal := p.schema(src("n Int @default(7)"))
		identity := p.schema(src("n Int @default(autoincrement())"))
		p.migrate(nil, literal)
		p.migrate(literal, identity)
		table := `"` + p.base + `_audit"."Item"`
		p.exec(`INSERT INTO ` + table + ` (id) VALUES (1)`)
		p.check("1", `SELECT n::text FROM `+table)
	})
}

func TestPostgresMigrationAddUniqueColumn(t *testing.T) {
	p := openPostgresMigrationDB(t)
	old := p.schema("model User {\n  id Int @id\n}\n")
	next := p.schema("model User {\n  id Int @id\n  email String? @unique\n}\n")
	p.migrate(nil, old)
	p.migrate(old, next)
	p.check("1", `SELECT count(*) FROM pg_constraint WHERE conrelid = '"User"'::regclass AND contype = 'u'`)
	p.check("1", `SELECT count(*) FROM pg_index WHERE indrelid = '"User"'::regclass AND indisunique AND NOT indisprimary`)
	p.exec(`INSERT INTO "User" VALUES (1, 'a@example.com')`)
	p.expectError("23505", `INSERT INTO "User" VALUES (2, 'a@example.com')`)
	p.down(old, next)
	p.check("0", `SELECT count(*) FROM pg_constraint WHERE conrelid = '"User"'::regclass AND contype = 'u'`)
}

func TestPostgresMigrationMovesTableSchema(t *testing.T) {
	p := openPostgresMigrationDB(t)
	src := func(schemaAttr string) string {
		return `
model Owner {
  id    Int    @id
  items Item[]
}
model Item {
  id      Int    @id @default(autoincrement())
  ownerId Int    @map("owner_id")
  owner   Owner  @relation(fields: [ownerId], references: [id])
  label   String @default("")
  @@index([label])
  ` + schemaAttr + `
  @@trigger(name: "item_label", timing: Before, event: Insert, body: "BEGIN\n  NEW.label := 'set';\n  RETURN NEW;\nEND;")
}
`
	}
	audit := p.schema(src(`@@schema("{{b}}_audit")`))
	archive := p.schema(src(`@@schema("{{b}}_archive")`))
	plain := p.schema(src(""))
	function := triggerddl.PostgresFunctionName("Item", "item_label")
	location := func() string {
		return p.query(`SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relname = 'Item' AND n.nspname LIKE $1 || '%'`, p.base) +
			"|" + p.query(`SELECT n.nspname FROM pg_proc f JOIN pg_namespace n ON n.oid = f.pronamespace WHERE f.proname = $1 AND n.nspname LIKE $2 || '%'`, function, p.base)
	}

	p.migrate(nil, audit)
	p.exec(`INSERT INTO "Owner" VALUES (1)`)
	p.exec(`INSERT INTO "` + p.base + `_audit"."Item" (owner_id) VALUES (1)`)

	p.migrate(audit, archive)
	if got, want := location(), p.base+"_archive|"+p.base+"_archive"; got != want {
		t.Fatalf("location = %q, want %q", got, want)
	}
	p.exec(`INSERT INTO "` + p.base + `_archive"."Item" (owner_id) VALUES (1)`)
	p.check("2 set", `SELECT count(*) || ' ' || min(label) FROM "`+p.base+`_archive"."Item"`)
	p.check("1", `SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexname = 'idx_Item_label'`, p.base+"_archive")
	p.expectError("23503", `INSERT INTO "`+p.base+`_archive"."Item" (owner_id) VALUES (99)`)

	p.down(audit, archive)
	if got, want := location(), p.base+"_audit|"+p.base+"_audit"; got != want {
		t.Fatalf("reverted location = %q, want %q", got, want)
	}

	// An empty target schema resolves to the search_path schema.
	p.migrate(audit, plain)
	if got, want := location(), p.base+"|"+p.base; got != want {
		t.Fatalf("location = %q, want %q", got, want)
	}
	p.exec(`INSERT INTO "Item" (owner_id) VALUES (1)`)
	// The identity continues; the rejected foreign key insert consumed 3.
	p.check("4", `SELECT max(id)::text FROM "Item"`)
	p.down(audit, plain)
	if got, want := location(), p.base+"_audit|"+p.base+"_audit"; got != want {
		t.Fatalf("reverted location = %q, want %q", got, want)
	}
}
