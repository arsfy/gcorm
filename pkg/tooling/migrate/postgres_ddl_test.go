package migrate

import (
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/triggerddl"
)

const postgresTestDatasource = `datasource db {
  provider = "postgresql"
  url      = "postgres://unused"
}
`

func compileMigrateSchema(t *testing.T, src string) *ir.Schema {
	t.Helper()
	result := compiler.CompileFile("schema.gcorm", []byte(src))
	if result.HasErrors() {
		t.Fatalf("compile schema: %v\n%s", result.Errors, src)
	}
	return result.Schema
}

func postgresUp(old, new *ir.Schema) string {
	return DDLGenerator{Dialect: "postgresql", Schema: new}.GenerateUp(Diff(old, new))
}

func postgresDown(old, new *ir.Schema) string {
	return DDLGenerator{Dialect: "postgresql", Schema: new}.GenerateDown(Diff(old, new))
}

func requireSQL(t *testing.T, sql string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing %q:\n%s", want, sql)
		}
	}
}

func rejectSQL(t *testing.T, sql string, rejects ...string) {
	t.Helper()
	for _, reject := range rejects {
		if strings.Contains(sql, reject) {
			t.Fatalf("SQL must not contain %q:\n%s", reject, sql)
		}
	}
}

func requireOrder(t *testing.T, sql string, parts ...string) {
	t.Helper()
	last := -1
	for _, part := range parts {
		idx := strings.Index(sql, part)
		if idx < 0 || idx < last {
			t.Fatalf("SQL must contain %q in order:\n%s", parts, sql)
		}
		last = idx
	}
}

// ---------------------------------------------------------------------------
// @@schema
// ---------------------------------------------------------------------------

func TestPostgresSchemaQualifiesTableDDL(t *testing.T) {
	schema := compileMigrateSchema(t, postgresTestDatasource+`
model Owner {
  id    Int    @id
  email String @unique
  @@schema("crm")
}
model Item {
  id      Int    @id @default(autoincrement())
  ownerId Int    @map("owner_id")
  owner   Owner  @relation(fields: [ownerId], references: [id])
  label   String
  @@index([label])
  @@schema("audit")
  @@trigger(name: "item_touch", timing: Before, event: Insert, body: "BEGIN RETURN NEW; END;")
}
model Plain {
  id Int @id
}
`)
	up := postgresUp(nil, schema)
	function := triggerddl.PostgresFunctionName("Item", "item_touch")
	requireSQL(t, up,
		`CREATE SCHEMA IF NOT EXISTS "audit";`,
		`CREATE SCHEMA IF NOT EXISTS "crm";`,
		`CREATE TABLE "crm"."Owner" (`,
		`CREATE TABLE "audit"."Item" (`,
		`CREATE TABLE "Plain" (`,
		`CREATE INDEX "idx_Item_label" ON "audit"."Item" ("label");`,
		`ALTER TABLE "audit"."Item" ADD CONSTRAINT "fk_Item_owner_id" FOREIGN KEY ("owner_id") REFERENCES "crm"."Owner" ("id");`,
		`CREATE OR REPLACE FUNCTION "audit"."`+function+`"()`,
		`CREATE TRIGGER "item_touch" BEFORE INSERT ON "audit"."Item" FOR EACH ROW EXECUTE FUNCTION "audit"."`+function+`"();`,
	)
	requireOrder(t, up, `CREATE SCHEMA IF NOT EXISTS "audit";`, `CREATE TABLE "crm"."Owner"`, `CREATE TABLE "audit"."Item"`)

	down := postgresDown(nil, schema)
	requireSQL(t, down,
		`DROP TABLE "audit"."Item";`,
		`DROP TABLE "crm"."Owner";`,
		`DROP TRIGGER IF EXISTS "item_touch" ON "audit"."Item";`,
		`DROP FUNCTION IF EXISTS "audit"."`+function+`"();`,
	)

	// Schemas already used by the source schema are not created again.
	next := compileMigrateSchema(t, postgresTestDatasource+`
model Owner {
  id    Int    @id
  email String @unique
  name  String?
  @@schema("crm")
}
model Item {
  id      Int    @id @default(autoincrement())
  ownerId Int    @map("owner_id")
  owner   Owner  @relation(fields: [ownerId], references: [id])
  label   String
  @@schema("audit")
  @@trigger(name: "item_touch", timing: Before, event: Insert, body: "BEGIN RETURN NEW; END;")
}
model Plain {
  id Int @id
}
`)
	up = postgresUp(schema, next)
	requireSQL(t, up,
		`ALTER TABLE "crm"."Owner" ADD COLUMN "name" TEXT;`,
		`DROP INDEX IF EXISTS "audit"."idx_Item_label";`,
	)
	rejectSQL(t, up, "CREATE SCHEMA")
}

func TestPostgresSchemaQualifiesConstraintChanges(t *testing.T) {
	gen := DDLGenerator{Dialect: "postgresql"}
	tests := []struct {
		name   string
		sql    string
		wants  []string
		reject string
	}{
		{"drop table", gen.changeToUp(Change{Type: DropTable, Model: "t", Schema: "s"}, &Changeset{}), []string{`DROP TABLE "s"."t";`}, ""},
		{"drop column", gen.changeToUp(Change{Type: DropColumn, Model: "t", Schema: "s", Field: "c"}, &Changeset{}), []string{`ALTER TABLE "s"."t" DROP COLUMN "c";`}, ""},
		{"alter null", gen.alterNullSQL(Change{Model: "t", Schema: "s", Field: "c", NewValue: "optional"}), []string{`ALTER TABLE "s"."t" ALTER COLUMN "c" DROP NOT NULL;`}, ""},
		{"add unique", gen.addUniqueSQL(Change{Model: "t", Schema: "s", Details: map[string]string{"fields": "c"}}), []string{`ALTER TABLE "s"."t" ADD CONSTRAINT "uq_t_c" UNIQUE ("c");`}, ""},
		{"drop unique", gen.dropUniqueSQL(Change{Model: "t", Schema: "s", Details: map[string]string{"fields": "c"}}), []string{`ALTER TABLE "s"."t" DROP CONSTRAINT IF EXISTS "uq_t_c";`, `ALTER TABLE "s"."t" DROP CONSTRAINT IF EXISTS "t_c_key";`}, ""},
		{"change pk", gen.changePKSQL(Change{Model: "t", Schema: "s", OldValue: "id", NewValue: "uid"}), []string{`ALTER TABLE "s"."t" DROP CONSTRAINT IF EXISTS "t_pkey";`, `ALTER TABLE "s"."t" ADD PRIMARY KEY ("uid");`}, ""},
		{"drop fk", gen.dropFKSQL(Change{Model: "t", Schema: "s", Details: map[string]string{"fields": "a"}}), []string{`ALTER TABLE "s"."t" DROP CONSTRAINT IF EXISTS "fk_t_a";`}, ""},
		{"add index", gen.addIndexSQL(Change{Model: "t", Schema: "s", NewValue: "i", Details: map[string]string{"fields": "c"}}), []string{`CREATE INDEX "i" ON "s"."t" ("c");`}, `"s"."i"`},
		{"drop index", gen.dropIndexSQL(Change{Model: "t", Schema: "s", OldValue: "i", Details: map[string]string{"fields": "c"}}), []string{`DROP INDEX IF EXISTS "s"."i";`}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireSQL(t, tt.sql, tt.wants...)
			if tt.reject != "" {
				rejectSQL(t, tt.sql, tt.reject)
			}
		})
	}
	// Schemas are PostgreSQL-only; other dialects ignore them.
	if got := (DDLGenerator{Dialect: "mysql"}).changeToUp(Change{Type: DropTable, Model: "t", Schema: "s"}, &Changeset{}); got != "DROP TABLE `t`;" {
		t.Fatalf("mysql drop table = %q", got)
	}
}

func TestDiffKeysModelsBySchema(t *testing.T) {
	a := testModel("AuditUser", testField("id", "Int"))
	a.DBName, a.Schema = "users", "audit"
	b := testModel("User", testField("id", "Int"))
	b.DBName = "users"
	schema := testSchema(a, b)

	cs := Diff(nil, schema)
	var creates []string
	for _, c := range cs.Changes {
		if c.Type == CreateTable {
			creates = append(creates, c.Schema+"."+c.Model)
		}
	}
	if strings.Join(creates, ",") != "audit.users,.users" {
		t.Fatalf("CreateTable changes = %v", creates)
	}
	up := DDLGenerator{Dialect: "postgresql", Schema: schema}.GenerateUp(cs)
	requireSQL(t, up, `CREATE TABLE "users" (`, `CREATE TABLE "audit"."users" (`)

	if got := Diff(schema, schema); len(got.Changes) != 0 {
		t.Fatalf("identical schemas produced changes: %#v", got.Changes)
	}

	// A column added to one of the two tables addresses only that table.
	a2 := testModel("AuditUser", testField("id", "Int"), testFieldOptional("note", "String"))
	a2.DBName, a2.Schema = "users", "audit"
	cs = Diff(schema, testSchema(a2, b))
	if len(cs.Changes) != 1 || cs.Changes[0].Type != AddColumn || cs.Changes[0].Schema != "audit" {
		t.Fatalf("changes = %#v", cs.Changes)
	}
}

func TestDiffMovesTableBetweenSchemas(t *testing.T) {
	trigger := `@@trigger(name: "item_touch", timing: Before, event: Insert, body: "BEGIN RETURN NEW; END;")`
	src := func(schemaAttr string) string {
		return postgresTestDatasource + `
model Owner {
  id Int @id
}
model Item {
  id      Int   @id
  ownerId Int   @map("owner_id")
  owner   Owner @relation(fields: [ownerId], references: [id])
  @@index([ownerId])
  ` + schemaAttr + `
  ` + trigger + `
}
`
	}
	old := compileMigrateSchema(t, src(`@@schema("audit")`))
	moved := compileMigrateSchema(t, src(`@@schema("archive")`))
	function := triggerddl.PostgresFunctionName("Item", "item_touch")

	cs := Diff(old, moved)
	if len(cs.Changes) != 1 || cs.Changes[0].Type != SetTableSchema || cs.Changes[0].OldValue != "audit" ||
		cs.Changes[0].NewValue != "archive" || cs.Changes[0].Rollback != ReviewRequired {
		t.Fatalf("changes = %#v", cs.Changes)
	}
	up := postgresUp(old, moved)
	requireSQL(t, up,
		`CREATE SCHEMA IF NOT EXISTS "archive";`,
		`ALTER TABLE "audit"."Item" SET SCHEMA "archive";`,
		`ALTER FUNCTION "audit"."`+function+`"() SET SCHEMA "archive";`,
	)
	rejectSQL(t, up, "DROP TABLE", "CREATE TABLE", "CREATE TRIGGER")
	down := postgresDown(old, moved)
	requireSQL(t, down,
		`ALTER TABLE "archive"."Item" SET SCHEMA "audit";`,
		`ALTER FUNCTION "archive"."`+function+`"() SET SCHEMA "audit";`,
	)

	// Moving to the search_path schema resolves the target at run time.
	toDefault := compileMigrateSchema(t, src(""))
	up = postgresUp(old, toDefault)
	requireSQL(t, up,
		"DO $gco$",
		`EXECUTE format('ALTER TABLE %I.%I SET SCHEMA %I', 'audit', 'Item', current_schema());`,
		`EXECUTE format('ALTER FUNCTION %I.%I() SET SCHEMA %I', 'audit', '`+function+`', current_schema());`,
	)
	steps := DDLGenerator{Dialect: "postgresql", Schema: toDefault}.UpSteps(Diff(old, toDefault))
	if len(steps) != 1 || !steps[0].SingleStatement {
		t.Fatalf("move to search_path steps = %#v", steps)
	}

	// Changes on a moved table run after the move and use the target schema;
	// a changed trigger is dropped at the source and created at the target.
	changed := compileMigrateSchema(t, strings.Replace(src(`@@schema("archive")`), "RETURN NEW;", "NEW.id := NEW.id; RETURN NEW;", 1))
	changed.Models[1].Fields = append(changed.Models[1].Fields, testFieldOptional("note", "String"))
	up = postgresUp(old, changed)
	requireOrder(t, up,
		`DROP TRIGGER IF EXISTS "item_touch" ON "audit"."Item";`,
		`ALTER TABLE "audit"."Item" SET SCHEMA "archive";`,
		`ALTER TABLE "archive"."Item" ADD COLUMN "note" TEXT;`,
		`CREATE TRIGGER "item_touch" BEFORE INSERT ON "archive"."Item"`,
	)
	rejectSQL(t, up, "ALTER FUNCTION")
}

func TestDiffForeignKeyTargetsIncludeSchema(t *testing.T) {
	src := func(ownerSchema string) string {
		return postgresTestDatasource + `
model Owner {
  id Int @id
  @@schema("` + ownerSchema + `")
}
model Item {
  id      Int   @id
  ownerId Int   @map("owner_id")
  owner   Owner @relation(fields: [ownerId], references: [id])
  @@schema("audit")
}
`
	}
	old, moved := compileMigrateSchema(t, src("crm")), compileMigrateSchema(t, src("sales"))
	cs := Diff(old, moved)
	var add, drop *Change
	for i := range cs.Changes {
		switch cs.Changes[i].Type {
		case AddFK:
			add = &cs.Changes[i]
		case DropFK:
			drop = &cs.Changes[i]
		}
	}
	if add == nil || drop == nil || add.Details["toSchema"] != "sales" || drop.Details["toSchema"] != "crm" || drop.Rollback != DestructiveRollback {
		t.Fatalf("changes = %#v", cs.Changes)
	}
	down := postgresDown(old, moved)
	// The restored constraint runs while Owner is still in its target schema.
	requireOrder(t, down,
		`ALTER TABLE "audit"."Item" ADD CONSTRAINT "fk_Item_owner_id" FOREIGN KEY ("owner_id") REFERENCES "sales"."Owner" ("id");`,
		`ALTER TABLE "sales"."Owner" SET SCHEMA "crm";`,
	)
}

// ---------------------------------------------------------------------------
// dbgenerated() and cuid()
// ---------------------------------------------------------------------------

func TestDDLDbGeneratedDefaultIsVerbatim(t *testing.T) {
	schema := compileMigrateSchema(t, postgresTestDatasource+`
model Item {
  id    Int    @id
  token String @default(dbgenerated("md5(concat('a', 'b', random()::text))"))
}
`)
	up := postgresUp(nil, schema)
	requireSQL(t, up, `"token" TEXT NOT NULL DEFAULT md5(concat('a', 'b', random()::text))`)

	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		gen := DDLGenerator{Dialect: dialect}
		if got := gen.defaultExpr("dbgenerated(coalesce(a, b))"); got != "coalesce(a, b)" {
			t.Fatalf("%s defaultExpr(dbgenerated) = %q", dialect, got)
		}
	}
	if got := dbGeneratedExpr(`"now()"`); got != "now()" {
		t.Fatalf("dbGeneratedExpr(quoted) = %q", got)
	}
	if got := dbGeneratedExpr(`"a" || "b"`); got != `"a" || "b"` {
		t.Fatalf("dbGeneratedExpr(identifiers) = %q", got)
	}

	// Changing the expression is an AlterDefault that emits it verbatim.
	next := compileMigrateSchema(t, postgresTestDatasource+`
model Item {
  id    Int    @id
  token String @default(dbgenerated("gen_random_uuid()::text"))
}
`)
	requireSQL(t, postgresUp(schema, next), `ALTER TABLE "Item" ALTER COLUMN "token" SET DEFAULT gen_random_uuid()::text;`)
}

func TestDDLGeneratedColumnIsNotAltered(t *testing.T) {
	generated := testFieldWithDefault("total", "Int", &ir.DefaultValue{IsFunction: true, FuncName: "dbgenerated", FuncArgs: []string{"price * 2"}})
	generated.IsGenerated = true
	target := testFieldWithDefault("total", "Int", &ir.DefaultValue{IsFunction: true, FuncName: "dbgenerated", FuncArgs: []string{"price*2"}})
	live := testSchema(testModel("items", testField("price", "Int"), generated))
	cs := Diff(live, testSchema(testModel("items", testField("price", "Int"), target)))
	if len(cs.Changes) != 0 {
		t.Fatalf("generated column default must not be altered: %#v", cs.Changes)
	}
	// Neither is its type; nullability still applies.
	bigger := testFieldOptional("total", "BigInt")
	cs = Diff(live, testSchema(testModel("items", testField("price", "Int"), bigger)))
	if len(cs.Changes) != 1 || cs.Changes[0].Type != AlterNull {
		t.Fatalf("generated column changes = %#v", cs.Changes)
	}
	got := DDLGenerator{Dialect: "postgresql"}.columnDefInternal(nil, generated, false)
	if got != `"total" INTEGER NOT NULL GENERATED ALWAYS AS (price * 2) STORED` {
		t.Fatalf("generated column definition = %q", got)
	}
}

func TestDDLCUIDHasNoDatabaseDefault(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		src := strings.Replace(postgresTestDatasource, "postgresql", dialect, 1) + `
model Item {
  id   String @id @default(cuid())
  code String @default(cuid())
}
`
		schema := compileMigrateSchema(t, src)
		up := DDLGenerator{Dialect: dialect, Schema: schema}.GenerateUp(Diff(nil, schema))
		rejectSQL(t, up, "DEFAULT", "cuid")
	}

	withCUID := testFieldWithDefault("code", "String", &ir.DefaultValue{IsFunction: true, FuncName: "cuid"})
	without := testField("code", "String")
	if cs := Diff(testSchema(testModel("items", without)), testSchema(testModel("items", withCUID))); len(cs.Changes) != 0 {
		t.Fatalf("cuid() target against a column without default must not change: %#v", cs.Changes)
	}
	literal := testFieldWithDefault("code", "String", &ir.DefaultValue{Value: "x", IsString: true})
	cs := Diff(testSchema(testModel("items", literal)), testSchema(testModel("items", withCUID)))
	if len(cs.Changes) != 1 || cs.Changes[0].Type != AlterDefault || cs.Changes[0].NewValue != "" {
		t.Fatalf("literal -> cuid() changes = %#v", cs.Changes)
	}
	requireSQL(t, DDLGenerator{Dialect: "postgresql"}.GenerateUp(cs), `ALTER TABLE "items" ALTER COLUMN "code" DROP DEFAULT;`)
}

// ---------------------------------------------------------------------------
// Enum and list column types and defaults
// ---------------------------------------------------------------------------

func TestDDLPostgresListColumnTypes(t *testing.T) {
	schema := compileMigrateSchema(t, postgresTestDatasource+`
enum Role {
  ADMIN @map("admin")
  USER
  @@map("role_kind")
}
model Item {
  id     Int      @id
  roles  Role[]
  codes  String[] @db.VarChar(10)
  role   Role
}
`)
	up := postgresUp(nil, schema)
	requireSQL(t, up, `"roles" "role_kind"[] NOT NULL`, `"codes" VARCHAR(10)[] NOT NULL`, `"role" "role_kind" NOT NULL`)
}

func TestDDLEnumDefaultsUseDatabaseLabels(t *testing.T) {
	schema := compileMigrateSchema(t, postgresTestDatasource+`
enum Role {
  ADMIN @map("admin")
  USER
}
model Item {
  id      Int        @id
  role    Role       @default(ADMIN)
  roles   Role[]     @default([ADMIN, USER])
  none    Role[]     @default([])
  tags    String[]   @default([])
  names   String[]   @default(["a", "1"])
  ids     UUID[]     @default(["7d9f1c2e-0000-4000-8000-000000000000"])
  times   DateTime[] @default(["2024-01-01T00:00:00Z"])
  counts  Int[]      @default([1, 2])
  codes   String[]   @db.VarChar(10) @default(["x"])
}
`)
	up := postgresUp(nil, schema)
	requireSQL(t, up,
		`"role" "Role" NOT NULL DEFAULT 'admin'`,
		`"roles" "Role"[] NOT NULL DEFAULT ARRAY['admin', 'USER']::"Role"[]`,
		`"none" "Role"[] NOT NULL DEFAULT '{}'::"Role"[]`,
		`"tags" TEXT[] NOT NULL DEFAULT '{}'::TEXT[]`,
		`"names" TEXT[] NOT NULL DEFAULT ARRAY['a', '1']::TEXT[]`,
		`"ids" UUID[] NOT NULL DEFAULT ARRAY['7d9f1c2e-0000-4000-8000-000000000000']::UUID[]`,
		`"times" TIMESTAMPTZ[] NOT NULL DEFAULT ARRAY['2024-01-01T00:00:00Z']::TIMESTAMPTZ[]`,
		`"counts" INTEGER[] NOT NULL DEFAULT ARRAY[1, 2]::INTEGER[]`,
		`"codes" VARCHAR(10)[] NOT NULL DEFAULT ARRAY['x']::VARCHAR(10)[]`,
	)

	// MySQL and SQLite store the same labels.
	mysql := compileMigrateSchema(t, strings.Replace(postgresTestDatasource, "postgresql", "mysql", 1)+`
enum Role {
  ADMIN @map("admin")
  USER
}
model Item {
  id   Int  @id
  role Role @default(ADMIN)
}
`)
	requireSQL(t, DDLGenerator{Dialect: "mysql", Schema: mysql}.GenerateUp(Diff(nil, mysql)), "`role` ENUM('admin', 'USER') NOT NULL DEFAULT 'admin'")
	requireSQL(t, DDLGenerator{Dialect: "sqlite", Schema: mysql}.GenerateUp(Diff(nil, mysql)), `"role" TEXT NOT NULL DEFAULT 'admin'`)
}

// ---------------------------------------------------------------------------
// ALTER COLUMN TYPE
// ---------------------------------------------------------------------------

func TestPostgresUsingCast(t *testing.T) {
	enum := func(list bool) columnType { return columnType{category: "enum", isList: list} }
	tests := []struct {
		name     string
		newType  string
		from, to columnType
		want     string
	}{
		{"text to int", "INTEGER", columnType{category: "text"}, columnType{category: "integer"}, `"c"::INTEGER`},
		{"int to bigint", "BIGINT", columnType{category: "integer"}, columnType{category: "integer"}, `"c"::BIGINT`},
		{"int to numeric", "DECIMAL(10,2)", columnType{category: "integer"}, columnType{category: "numeric"}, `"c"::DECIMAL(10,2)`},
		{"bool to int", "INTEGER", columnType{category: "bool"}, columnType{category: "integer"}, `"c"::INTEGER`},
		{"uuid to text", "TEXT", columnType{category: "UUID"}, columnType{category: "text"}, `"c"::TEXT`},
		{"datetime to int", "INTEGER", columnType{category: "DateTime"}, columnType{category: "integer"}, `"c"::text::INTEGER`},
		{"text to enum", `"Role"`, columnType{category: "text"}, enum(false), `"c"::text::"Role"`},
		{"enum to enum", `"Kind"`, enum(false), enum(false), `"c"::text::"Kind"`},
		{"enum to text", "TEXT", enum(false), columnType{category: "text"}, `"c"::text::TEXT`},
		{"text list to enum list", `"Role"[]`, columnType{category: "text", isList: true}, enum(true), `"c"::text::"Role"[]`},
		{"scalar to list", "TEXT[]", columnType{category: "text"}, columnType{category: "text", isList: true}, `ARRAY["c"::TEXT]`},
		{"list to scalar", "TEXT", columnType{category: "text", isList: true}, columnType{category: "text"}, `"c"::text::TEXT`},
		{"unknown source", "BIGINT", columnType{}, columnType{category: "integer"}, `"c"::BIGINT`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresUsingCast(`"c"`, tt.newType, tt.from, tt.to); got != tt.want {
				t.Fatalf("postgresUsingCast() = %q, want %q", got, tt.want)
			}
		})
	}
	if got := columnTypeOf(nil, "String@db.VarChar(10)[]"); got != (columnType{category: "text", isList: true}) {
		t.Fatalf("columnTypeOf(signature) = %#v", got)
	}
}

func TestPostgresAlterTypeHandlesDefaults(t *testing.T) {
	src := func(field string) string {
		return postgresTestDatasource + `
enum Status {
  ACTIVE @map("active")
  INACTIVE
}
model Item {
  id Int @id
  ` + field + `
}
`
	}
	t.Run("string to enum with new default", func(t *testing.T) {
		old := compileMigrateSchema(t, src(`status String @default("on")`))
		next := compileMigrateSchema(t, src(`status Status @default(ACTIVE)`))
		up := postgresUp(old, next)
		requireOrder(t, up,
			`ALTER TABLE "Item" ALTER COLUMN "status" DROP DEFAULT;`,
			`ALTER TABLE "Item" ALTER COLUMN "status" TYPE "Status" USING "status"::text::"Status";`,
			`ALTER TABLE "Item" ALTER COLUMN "status" SET DEFAULT 'active';`,
		)
		if strings.Count(up, "SET DEFAULT") != 1 {
			t.Fatalf("default must be set once:\n%s", up)
		}
		// The default is set after the column is converted back.
		down := postgresDown(old, next)
		requireOrder(t, down,
			`ALTER TABLE "Item" ALTER COLUMN "status" DROP DEFAULT;`,
			`ALTER TABLE "Item" ALTER COLUMN "status" TYPE TEXT USING "status"::text::TEXT;`,
			`ALTER TABLE "Item" ALTER COLUMN "status" SET DEFAULT 'on';`,
		)
		if strings.Count(down, "SET DEFAULT") != 1 {
			t.Fatalf("default must be set once:\n%s", down)
		}
	})
	t.Run("same default is restored after the type change", func(t *testing.T) {
		old := compileMigrateSchema(t, src(`code String @default("0")`))
		next := compileMigrateSchema(t, src(`code Int @default(0)`))
		up := postgresUp(old, next)
		requireOrder(t, up,
			`ALTER TABLE "Item" ALTER COLUMN "code" DROP DEFAULT;`,
			`ALTER TABLE "Item" ALTER COLUMN "code" TYPE INTEGER USING "code"::INTEGER;`,
			`ALTER TABLE "Item" ALTER COLUMN "code" SET DEFAULT 0;`,
		)
		requireOrder(t, postgresDown(old, next),
			`ALTER TABLE "Item" ALTER COLUMN "code" DROP DEFAULT;`,
			`ALTER TABLE "Item" ALTER COLUMN "code" TYPE TEXT USING "code"::TEXT;`,
			`ALTER TABLE "Item" ALTER COLUMN "code" SET DEFAULT '0';`,
		)
	})
	t.Run("default dropped with the type change", func(t *testing.T) {
		old := compileMigrateSchema(t, src(`code String @default("0")`))
		next := compileMigrateSchema(t, src(`code Int`))
		up := postgresUp(old, next)
		if up != `ALTER TABLE "Item" ALTER COLUMN "code" DROP DEFAULT;`+"\n"+`ALTER TABLE "Item" ALTER COLUMN "code" TYPE INTEGER USING "code"::INTEGER;` {
			t.Fatalf("up = %q", up)
		}
	})
	t.Run("identity column keeps its identity", func(t *testing.T) {
		old := compileMigrateSchema(t, src(`n Int @default(autoincrement())`))
		next := compileMigrateSchema(t, src(`n BigInt @default(autoincrement())`))
		up := postgresUp(old, next)
		if up != `ALTER TABLE "Item" ALTER COLUMN "n" TYPE BIGINT USING "n"::BIGINT;` {
			t.Fatalf("identity type change = %q", up)
		}
	})
}

// ---------------------------------------------------------------------------
// Identity transitions
// ---------------------------------------------------------------------------

func TestPostgresIdentityTransitions(t *testing.T) {
	gen := DDLGenerator{Dialect: "postgresql"}
	field := func(d *ir.DefaultValue) *ir.Field { return testFieldWithDefault("id", "Int", d) }
	identity := &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}
	literal := &ir.DefaultValue{Value: "7", IsNumber: true}
	setval := `SELECT setval(pg_get_serial_sequence('"s"."t"', 'id'), GREATEST(COALESCE(MAX("id"), 0), 0) + 1, false) FROM "s"."t";`
	tests := []struct {
		name   string
		change Change
		want   string
	}{
		// DROP DEFAULT also removes the nextval() default of serial columns,
		// which introspect as autoincrement but are not identity columns.
		{"identity to none", Change{OldValue: "autoincrement()", OldField: field(identity), NewField: field(nil)},
			`ALTER TABLE "s"."t" ALTER COLUMN "id" DROP IDENTITY IF EXISTS;` + "\n" + `ALTER TABLE "s"."t" ALTER COLUMN "id" DROP DEFAULT;`},
		{"identity to literal", Change{OldValue: "autoincrement()", NewValue: "7", OldField: field(identity), NewField: field(literal)},
			`ALTER TABLE "s"."t" ALTER COLUMN "id" DROP IDENTITY IF EXISTS;` + "\n" + `ALTER TABLE "s"."t" ALTER COLUMN "id" SET DEFAULT 7;`},
		{"literal to identity", Change{OldValue: "7", NewValue: "autoincrement()", OldField: field(literal), NewField: field(identity)},
			`ALTER TABLE "s"."t" ALTER COLUMN "id" DROP DEFAULT;` + "\n" + `ALTER TABLE "s"."t" ALTER COLUMN "id" ADD GENERATED BY DEFAULT AS IDENTITY;` + "\n" + setval},
		{"none to identity", Change{NewValue: "autoincrement()", OldField: field(nil), NewField: field(identity)},
			`ALTER TABLE "s"."t" ALTER COLUMN "id" ADD GENERATED BY DEFAULT AS IDENTITY;` + "\n" + setval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.change.Type, tt.change.Model, tt.change.Schema, tt.change.Field = AlterDefault, "t", "s", "id"
			if got := gen.alterDefaultSQL(tt.change); got != tt.want {
				t.Fatalf("alterDefaultSQL() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
	// Down reverses through the same transitions.
	down := gen.changeToDown(Change{Type: AlterDefault, Model: "t", Schema: "s", Field: "id", OldValue: "7", NewValue: "autoincrement()",
		OldField: field(literal), NewField: field(identity)}, &Changeset{})
	if down != tests[1].want {
		t.Fatalf("down = %q", down)
	}
}

// ---------------------------------------------------------------------------
// Enum changes
// ---------------------------------------------------------------------------

func TestPostgresEnumAddValueKeepsOrderAndRequiresCommit(t *testing.T) {
	src := func(values string) string {
		return postgresTestDatasource + "enum Role {\n" + values + "\n}\nmodel Item {\n  id Int @id\n  role Role @default(USER)\n}\n"
	}
	old := compileMigrateSchema(t, src("USER\nGUEST"))
	next := compileMigrateSchema(t, src("OWNER\nUSER\nADMIN @map(\"admin\")\nGUEST\nLAST"))
	next.Models[0].Fields = append(next.Models[0].Fields, testFieldWithDefault("tier", "", &ir.DefaultValue{Value: "ADMIN"}))
	next.Models[0].Fields[2].Type, next.Models[0].Fields[2].EnumType = ir.FieldKindEnum, "Role"

	cs := Diff(old, next)
	steps := DDLGenerator{Dialect: "postgresql", Schema: next}.UpSteps(cs)
	want := []string{
		`ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'OWNER' BEFORE 'USER';`,
		`ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'admin' AFTER 'USER';`,
		`ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'LAST' AFTER 'GUEST';`,
	}
	if len(steps) != len(want)+1 {
		t.Fatalf("steps = %#v", steps)
	}
	for i, sql := range want {
		if steps[i].SQL != sql || !steps[i].RequiresCommit {
			t.Fatalf("step %d = %#v, want %q requiring commit", i, steps[i], sql)
		}
	}
	if steps[3].RequiresCommit || steps[3].SQL != `ALTER TABLE "Item" ADD COLUMN "tier" "Role" NOT NULL DEFAULT 'admin';` {
		t.Fatalf("last step = %#v", steps[3])
	}
	up := DDLGenerator{Dialect: "postgresql", Schema: next}.GenerateUp(cs)
	requireOrder(t, up, requiresCommitStartComment, want[0], want[2], requiresCommitEndComment, `ADD COLUMN "tier"`)

	// Reverting added values recreates the type without them, after the
	// column that uses a new value is dropped.
	down := DDLGenerator{Dialect: "postgresql", Schema: next}.GenerateDown(cs)
	rejectSQL(t, down, `COLUMN "tier" TYPE`)
	requireOrder(t, down,
		`ALTER TABLE "Item" DROP COLUMN "tier";`,
		`ALTER TYPE "Role" RENAME TO "Role__gco_old";`,
		`CREATE TYPE "Role" AS ENUM ('USER', 'GUEST');`,
		`ALTER TABLE "Item" ALTER COLUMN "role" DROP DEFAULT;`,
		`ALTER TABLE "Item" ALTER COLUMN "role" TYPE "Role" USING "role"::text::"Role";`,
		`ALTER TABLE "Item" ALTER COLUMN "role" SET DEFAULT 'USER';`,
		`DROP TYPE "Role__gco_old";`,
	)
}

func TestPostgresEnumRenameAndRemoveValues(t *testing.T) {
	src := func(values, mapAttr string) string {
		return postgresTestDatasource + "enum Role {\n" + values + "\n" + mapAttr + "\n}\n" + `
model Item {
  id    Int    @id
  role  Role   @default(ADMIN)
  roles Role[]
  @@schema("audit")
}
`
	}
	old := compileMigrateSchema(t, src("ADMIN @map(\"admin\")\nUSER\nLEGACY", ""))

	t.Run("value map change renames the label", func(t *testing.T) {
		next := compileMigrateSchema(t, src("ADMIN @map(\"adm\")\nUSER\nLEGACY", ""))
		cs := Diff(old, next)
		steps := DDLGenerator{Dialect: "postgresql", Schema: next}.UpSteps(cs)
		if len(steps) != 1 || !steps[0].RequiresCommit || steps[0].SQL != `ALTER TYPE "Role" RENAME VALUE 'admin' TO 'adm';` {
			t.Fatalf("steps = %#v", steps)
		}
		requireSQL(t, postgresDown(old, next), `ALTER TYPE "Role" RENAME VALUE 'adm' TO 'admin';`)
	})

	t.Run("value removal recreates the type", func(t *testing.T) {
		next := compileMigrateSchema(t, src("ADMIN @map(\"adm\")\nUSER", ""))
		cs := Diff(old, next)
		if !hasChangeRollback(cs, AlterEnum, "Role", DestructiveRollback) {
			t.Fatalf("changes = %#v", cs.Changes)
		}
		steps := DDLGenerator{Dialect: "postgresql", Schema: next}.UpSteps(cs)
		if len(steps) != 2 || !steps[0].RequiresCommit || steps[1].RequiresCommit {
			t.Fatalf("steps = %#v", steps)
		}
		requireOrder(t, steps[1].SQL,
			`ALTER TYPE "Role" RENAME TO "Role__gco_old";`,
			`CREATE TYPE "Role" AS ENUM ('adm', 'USER');`,
			`ALTER TABLE "audit"."Item" ALTER COLUMN "role" DROP DEFAULT;`,
			`ALTER TABLE "audit"."Item" ALTER COLUMN "role" TYPE "Role" USING "role"::text::"Role";`,
			`ALTER TABLE "audit"."Item" ALTER COLUMN "role" SET DEFAULT 'adm';`,
			`ALTER TABLE "audit"."Item" ALTER COLUMN "roles" TYPE "Role"[] USING "roles"::text::"Role"[];`,
			`DROP TYPE "Role__gco_old";`,
		)
		// The removed value is added back at its position.
		requireOrder(t, postgresDown(old, next),
			requiresCommitStartComment,
			`ALTER TYPE "Role" RENAME VALUE 'adm' TO 'admin';`,
			`ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'LEGACY' AFTER 'USER';`,
		)
	})

	t.Run("enum map change renames the type", func(t *testing.T) {
		next := compileMigrateSchema(t, src("ADMIN @map(\"admin\")\nUSER\nLEGACY\nGUEST", `@@map("role_kind")`))
		cs := Diff(old, next)
		if !hasChange(cs, RenameEnum, "Role", "") {
			t.Fatalf("changes = %#v", cs.Changes)
		}
		steps := DDLGenerator{Dialect: "postgresql", Schema: next}.UpSteps(cs)
		if len(steps) != 2 || steps[0].SQL != `ALTER TYPE "Role" RENAME TO "role_kind";` ||
			steps[1].SQL != `ALTER TYPE "role_kind" ADD VALUE IF NOT EXISTS 'GUEST' AFTER 'LEGACY';` ||
			!steps[0].RequiresCommit || !steps[1].RequiresCommit {
			t.Fatalf("steps = %#v", steps)
		}
		requireOrder(t, postgresDown(old, next),
			`ALTER TYPE "role_kind" RENAME TO "role_kind__gco_old";`,
			`CREATE TYPE "role_kind" AS ENUM ('admin', 'USER', 'LEGACY');`,
			`ALTER TYPE "role_kind" RENAME TO "Role";`,
		)
	})

	t.Run("renaming a value but keeping its label changes nothing", func(t *testing.T) {
		next := compileMigrateSchema(t, strings.Replace(src("ADMINISTRATOR @map(\"admin\")\nUSER\nLEGACY", ""), "@default(ADMIN)", "@default(ADMINISTRATOR)", 1))
		for _, c := range Diff(old, next).Changes {
			if c.Type == AlterEnum {
				t.Fatalf("label-preserving rename produced %#v", c)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Identifier limits
// ---------------------------------------------------------------------------

func TestLimitIdentifier(t *testing.T) {
	short := "idx_users_" + strings.Repeat("a", 53)
	if len(short) != 63 || LimitIdentifier(short) != short {
		t.Fatalf("63-byte name must be unchanged: %q", LimitIdentifier(short))
	}
	long := short + "b"
	got := LimitIdentifier(long)
	if len(got) != 63 || !strings.HasPrefix(got, short[:54]+"_") || got == LimitIdentifier(short+"c") {
		t.Fatalf("LimitIdentifier(64 bytes) = %q", got)
	}
	// The prefix is cut at a UTF-8 boundary.
	multi := "idx_" + strings.Repeat("é", 40)
	got = LimitIdentifier(multi)
	if len(got) > 63 || !strings.HasPrefix(got, "idx_"+strings.Repeat("é", 25)+"_") {
		t.Fatalf("LimitIdentifier(multibyte) = %q", got)
	}
	if got := DefaultForeignKeyName("t", []string{"a", "b"}); got != "fk_t_a_b" {
		t.Fatalf("DefaultForeignKeyName() = %q", got)
	}
}

func TestPostgresGeneratedConstraintNames(t *testing.T) {
	table := strings.Repeat("t", 70)
	if got := PostgresPrimaryKeyName(table); got != strings.Repeat("t", 58)+"_pkey" {
		t.Fatalf("PostgresPrimaryKeyName() = %q", got)
	}
	if got := PostgresPrimaryKeyName("users"); got != "users_pkey" {
		t.Fatalf("PostgresPrimaryKeyName(users) = %q", got)
	}
	if got := PostgresUniqueKeyName("users", []string{"a", "b"}); got != "users_a_b_key" {
		t.Fatalf("PostgresUniqueKeyName() = %q", got)
	}
	// The longer part is truncated first.
	got := PostgresUniqueKeyName("orders", []string{strings.Repeat("c", 40), strings.Repeat("d", 40)})
	if len(got) != 63 || !strings.HasPrefix(got, "orders_"+strings.Repeat("c", 40)+"_dddd") || !strings.HasSuffix(got, "_key") {
		t.Fatalf("PostgresUniqueKeyName(long) = %q", got)
	}
	got = postgresMakeObjectName(strings.Repeat("é", 40), "", "pkey")
	if got != strings.Repeat("é", 29)+"_pkey" {
		t.Fatalf("postgresMakeObjectName(multibyte) = %q", got)
	}
}

func TestDDLLongGeneratedNames(t *testing.T) {
	longColumn := strings.Repeat("c", 60)
	model := testModel("orders", testField("id", "Int"), testField(longColumn, "Int"), testField("other", "Int"))
	model.Indexes = []*ir.Index{{Fields: []string{longColumn}}, {Fields: []string{longColumn, "other"}, IsUnique: true}}
	model.UniqueConstraints = []*ir.UniqueConstraint{{Fields: []string{"other", longColumn}}}
	model.Relations = []*ir.Relation{testRelation("orders", "orders", []string{longColumn, "other"}, []string{"id", "other"})}
	schema := testSchema(model)
	up := DDLGenerator{Dialect: "postgresql", Schema: schema}.GenerateUp(Diff(nil, schema))
	idx := LimitIdentifier("idx_orders_" + longColumn)
	uq := LimitIdentifier("uq_orders_" + longColumn + "_other")
	constraint := LimitIdentifier("uq_orders_other_" + longColumn)
	fk := LimitIdentifier("fk_orders_" + longColumn + "_other")
	for _, name := range []string{idx, uq, constraint, fk} {
		if len(name) > 63 {
			t.Fatalf("generated name %q exceeds 63 bytes", name)
		}
	}
	requireSQL(t, up, `CREATE INDEX "`+idx+`"`, `CREATE UNIQUE INDEX "`+uq+`"`, `CONSTRAINT "`+constraint+`" UNIQUE`, `ADD CONSTRAINT "`+fk+`" FOREIGN KEY`)

	// Drops cover names created by older versions.
	gen := DDLGenerator{Dialect: "postgresql"}
	requireSQL(t, gen.dropIndexSQL(Change{Model: "orders", OldValue: idx, Details: map[string]string{"fields": longColumn}}),
		`DROP INDEX IF EXISTS "`+idx+`";`, `DROP INDEX IF EXISTS "idx_orders_`+longColumn+`";`)
	requireSQL(t, gen.dropUniqueSQL(Change{Model: "orders", Details: map[string]string{"fields": "other," + longColumn}}),
		`DROP CONSTRAINT IF EXISTS "`+constraint+`";`,
		`DROP CONSTRAINT IF EXISTS "uq_orders_other_`+longColumn+`";`,
		`DROP CONSTRAINT IF EXISTS "`+PostgresUniqueKeyName("orders", []string{"other", longColumn})+`";`)
	requireSQL(t, gen.dropFKSQL(Change{Model: "t", Details: map[string]string{"fields": "a,b"}}),
		`DROP CONSTRAINT IF EXISTS "fk_t_a_b";`, `DROP CONSTRAINT IF EXISTS "fk_t_a,b";`)
	if got := gen.dropFKSQL(Change{Model: "t", Details: map[string]string{"fields": "a,b", "name": "custom"}}); got != `ALTER TABLE "t" DROP CONSTRAINT IF EXISTS "custom";` {
		t.Fatalf("named FK drop = %q", got)
	}
	// A change of the constraint name length does not churn short names.
	if got := effectiveIndexName("postgresql", model, &ir.Index{Fields: []string{"other"}}); got != "idx_orders_other" {
		t.Fatalf("short index name = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Adding a unique column
// ---------------------------------------------------------------------------

func TestAddUniqueColumnCreatesOneConstraint(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			email := testFieldOptional("email", "String")
			email.IsUnique = true
			old := testSchema(testModel("users", testField("id", "Int")))
			next := testSchema(testModel("users", testField("id", "Int"), email))
			up := DDLGenerator{Dialect: dialect, Schema: next}.GenerateUp(Diff(old, next))
			if count := strings.Count(up, "UNIQUE"); count != 1 {
				t.Fatalf("UNIQUE appears %d times:\n%s", count, up)
			}
		})
	}
}
