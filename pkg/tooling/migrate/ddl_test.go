package migrate

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// SQLite table rebuild
// ---------------------------------------------------------------------------

func TestDDLSQLiteRebuildTable(t *testing.T) {
	oldUser := testModel("User", testField("id", "Int"), testField("name", "String"), testField("age", "Int"))
	newUser := testModel("User", testField("id", "Int"), testField("name", "String"), testField("age", "String"))
	newUser.Indexes = []*ir.Index{{Name: "idx_users_name", Fields: []string{"name"}}}

	cs := Diff(testSchema(oldUser), testSchema(newUser))
	gen := DDLGenerator{Dialect: "sqlite", Schema: cs.New}
	up := gen.GenerateUp(cs)

	for _, want := range []string{
		"PRAGMA defer_foreign_keys = ON;",
		`CREATE TABLE "__gco_new_User"`,
		`INSERT INTO "__gco_new_User" ("id", "name", "age") SELECT "id", "name", "age" FROM "User";`,
		`DROP TABLE "User";`,
		`ALTER TABLE "__gco_new_User" RENAME TO "User";`,
		`CREATE INDEX "idx_users_name" ON "User" ("name");`,
	} {
		if !strings.Contains(up, want) {
			t.Fatalf("sqlite rebuild SQL missing %q:\n%s", want, up)
		}
	}

	// Apply the rebuild to a real SQLite database and verify data survives.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, stmt := range []string{
		`CREATE TABLE "User" (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`,
		`INSERT INTO "User" (name, age) VALUES ('alice', 30)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if _, err := db.Exec(up); err != nil {
		t.Fatalf("apply rebuild SQL: %v\n%s", err, up)
	}

	var name, age string
	if err := db.QueryRow(`SELECT name, age FROM "User"`).Scan(&name, &age); err != nil {
		t.Fatalf("query rebuilt table: %v", err)
	}
	if name != "alice" || age != "30" {
		t.Fatalf("rebuilt row = (%q, %q), want (alice, 30)", name, age)
	}

	var indexName string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_users_name'`).Scan(&indexName)
	if err != nil {
		t.Fatalf("recreated index missing: %v", err)
	}
}

func TestDDLSQLiteRebuildTableWithoutOldModel(t *testing.T) {
	cs := &Changeset{
		Changes: []Change{{Type: DropColumn, Model: "User", Field: "age"}},
		New:     testSchema(testModel("User", testField("id", "Int"), testField("name", "String"))),
	}

	up := DDLGenerator{Dialect: "sqlite", Schema: cs.New}.GenerateUp(cs)

	if !strings.Contains(up, `CREATE TABLE "__gco_new_User"`) {
		t.Fatalf("expected rebuild for dropped column:\n%s", up)
	}
	if strings.Contains(up, `INSERT INTO "__gco_new_User"`) {
		t.Fatalf("rebuild without old schema must not copy data:\n%s", up)
	}
}

func TestDDLSQLiteRebuildTableUnavailableForUnknownModel(t *testing.T) {
	cs := &Changeset{
		Changes: []Change{{Type: AlterType, Model: "Ghost", Field: "age", NewValue: "String"}},
		New:     testSchema(),
	}

	up := DDLGenerator{Dialect: "sqlite", Schema: cs.New}.GenerateUp(cs)

	if !strings.Contains(up, "-- SQLite table rebuild unavailable for Ghost") {
		t.Fatalf("expected unavailable rebuild comment:\n%s", up)
	}
}

func TestDDLSQLiteInlinesForeignKeyForCreatedTable(t *testing.T) {
	user := testModel("User", testField("id", "String"))
	user.DBName = "users"
	post := testModel("Post", testField("id", "String"), testField("authorId", "String"))
	post.DBName = "posts"
	post.Relations = []*ir.Relation{testRelation("Post", "User", []string{"authorId"}, []string{"id"})}

	cs := Diff(&ir.Schema{}, testSchema(post, user))
	sql := DDLGenerator{Dialect: "sqlite", Schema: cs.New}.GenerateUp(cs)

	if !strings.Contains(sql, `FOREIGN KEY ("author_id") REFERENCES "users" ("id")`) {
		t.Fatalf("expected inline foreign key in CREATE TABLE:\n%s", sql)
	}
	if strings.Contains(sql, "ADD CONSTRAINT") {
		t.Fatalf("sqlite must not emit ALTER TABLE ADD CONSTRAINT for created tables:\n%s", sql)
	}
}

// ---------------------------------------------------------------------------
// Enum DDL
// ---------------------------------------------------------------------------

func TestDDLPostgresEnumLifecycle(t *testing.T) {
	old := &ir.Schema{Enums: []*ir.Enum{
		{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}, {Name: "LEGACY"}}},
		{Name: "Retired", Values: []*ir.EnumValue{{Name: "X"}}},
	}}
	new := &ir.Schema{Enums: []*ir.Enum{
		{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}, {Name: "ADMIN"}}},
		{Name: "Status", DBName: "task_status", Values: []*ir.EnumValue{{Name: "OPEN", DBName: "open"}}},
	}}

	cs := Diff(old, new)
	sql := DDLGenerator{Dialect: "postgresql", Schema: new}.GenerateUp(cs)

	for _, want := range []string{
		`CREATE TYPE "task_status" AS ENUM ('open');`,
		`ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'ADMIN';`,
		`DROP TYPE "Retired";`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("postgres enum SQL missing %q:\n%s", want, sql)
		}
	}
	// Removed values cannot be dropped from a PostgreSQL enum.
	if strings.Contains(sql, "LEGACY") {
		t.Fatalf("removed enum value must not appear in enum DDL:\n%s", sql)
	}
	// CreateEnum and AlterEnum changes emit no per-change statements on postgres.
	if strings.Contains(sql, "ALTER TYPE \"Role\" ADD VALUE IF NOT EXISTS 'USER';") {
		t.Fatalf("existing enum value must not be re-added:\n%s", sql)
	}
}

func TestDDLAlterEnumMySQLRefreshesColumns(t *testing.T) {
	roleField := &ir.Field{Name: "role", Type: ir.FieldKindEnum, EnumType: "Role"}
	user := testModel("users", testField("id", "Int"), roleField)
	schema := &ir.Schema{
		Models: []*ir.Model{user},
		Enums:  []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}, {Name: "ADMIN"}}}},
	}
	change := Change{Type: AlterEnum, Model: "Role"}

	got := DDLGenerator{Dialect: "mysql", Schema: schema}.changeToUp(change, &Changeset{})
	want := "ALTER TABLE `users` MODIFY COLUMN `role` ENUM('USER', 'ADMIN') NOT NULL;"
	if got != want {
		t.Fatalf("mysql alter enum SQL = %q, want %q", got, want)
	}

	if got := (DDLGenerator{Dialect: "mysql"}).changeToUp(change, &Changeset{}); got != "" {
		t.Fatalf("mysql alter enum without schema = %q, want empty", got)
	}
	for _, dialect := range []string{"postgresql", "sqlite"} {
		if got := (DDLGenerator{Dialect: dialect}).changeToUp(change, &Changeset{}); got != "" {
			t.Fatalf("%s alter enum SQL = %q, want empty", dialect, got)
		}
	}
}

func TestDDLDropEnumNonPostgres(t *testing.T) {
	change := Change{Type: DropEnum, Model: "Role"}
	for _, dialect := range []string{"mysql", "sqlite"} {
		if got := (DDLGenerator{Dialect: dialect}).changeToUp(change, &Changeset{}); got != "" {
			t.Fatalf("%s drop enum SQL = %q, want empty", dialect, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Alter column DDL by dialect
// ---------------------------------------------------------------------------

func TestDDLAlterTypeSQLByDialect(t *testing.T) {
	field := &ir.Field{Name: "age", Type: ir.FieldKindScalar, ScalarType: "BigInt"}
	tests := []struct {
		name    string
		dialect string
		change  Change
		want    string
	}{
		{
			name:    "postgres",
			dialect: "postgresql",
			change:  Change{Type: AlterType, Model: "users", Field: "age", NewValue: "BigInt"},
			want:    `ALTER TABLE "users" ALTER COLUMN "age" TYPE BIGINT;`,
		},
		{
			name:    "mysql with full field definition",
			dialect: "mysql",
			change:  Change{Type: AlterType, Model: "users", Field: "age", NewValue: "BigInt", NewField: field},
			want:    "ALTER TABLE `users` MODIFY COLUMN `age` BIGINT NOT NULL;",
		},
		{
			name:    "mysql without field definition",
			dialect: "mysql",
			change:  Change{Type: AlterType, Model: "users", Field: "age", NewValue: "BigInt"},
			want:    "ALTER TABLE `users` MODIFY COLUMN `age` BIGINT;",
		},
		{
			name:    "sqlite",
			dialect: "sqlite",
			change:  Change{Type: AlterType, Model: "users", Field: "age", NewValue: "BigInt"},
			want:    "-- SQLite: ALTER COLUMN not supported; table rebuild required for users.age to INTEGER",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DDLGenerator{Dialect: tt.dialect}.alterTypeSQL(tt.change)
			if got != tt.want {
				t.Fatalf("alterTypeSQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDDLAlterDefaultSQLByDialect(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
		change  Change
		want    string
	}{
		{
			name:    "postgres drop default",
			dialect: "postgresql",
			change:  Change{Type: AlterDefault, Model: "users", Field: "age"},
			want:    `ALTER TABLE "users" ALTER COLUMN "age" DROP DEFAULT;`,
		},
		{
			name:    "mysql drop default",
			dialect: "mysql",
			change:  Change{Type: AlterDefault, Model: "users", Field: "age"},
			want:    "ALTER TABLE `users` ALTER COLUMN `age` DROP DEFAULT;",
		},
		{
			name:    "sqlite drop default unsupported",
			dialect: "sqlite",
			change:  Change{Type: AlterDefault, Model: "users", Field: "age"},
			want:    "-- SQLite: DROP DEFAULT not supported for users.age",
		},
		{
			name:    "postgres autoincrement",
			dialect: "postgresql",
			change:  Change{Type: AlterDefault, Model: "users", Field: "id", NewValue: "autoincrement()"},
			want:    `ALTER TABLE "users" ALTER COLUMN "id" ADD GENERATED BY DEFAULT AS IDENTITY;`,
		},
		{
			name:    "mysql autoincrement needs full definition",
			dialect: "mysql",
			change:  Change{Type: AlterDefault, Model: "users", Field: "id", NewValue: "autoincrement()"},
			want:    "-- MySQL: adding AUTO_INCREMENT requires MODIFY COLUMN with the full column definition for users.id",
		},
		{
			name:    "sqlite autoincrement needs rebuild",
			dialect: "sqlite",
			change:  Change{Type: AlterDefault, Model: "users", Field: "id", NewValue: "autoincrement()"},
			want:    "-- SQLite: adding AUTOINCREMENT requires table rebuild for users.id",
		},
		{
			name:    "postgres set default",
			dialect: "postgresql",
			change:  Change{Type: AlterDefault, Model: "users", Field: "age", NewValue: "5"},
			want:    `ALTER TABLE "users" ALTER COLUMN "age" SET DEFAULT 5;`,
		},
		{
			name:    "mysql set default",
			dialect: "mysql",
			change:  Change{Type: AlterDefault, Model: "users", Field: "age", NewValue: "5"},
			want:    "ALTER TABLE `users` ALTER COLUMN `age` SET DEFAULT 5;",
		},
		{
			name:    "sqlite set default unsupported",
			dialect: "sqlite",
			change:  Change{Type: AlterDefault, Model: "users", Field: "age", NewValue: "5"},
			want:    "-- SQLite: SET DEFAULT not supported inline for users.age",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DDLGenerator{Dialect: tt.dialect}.alterDefaultSQL(tt.change)
			if got != tt.want {
				t.Fatalf("alterDefaultSQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Down migration generation
// ---------------------------------------------------------------------------

func TestDDLChangeToDownReversesChanges(t *testing.T) {
	tests := []struct {
		name   string
		change Change
		want   []string
	}{
		{
			name:   "alter type",
			change: Change{Type: AlterType, Model: "users", Field: "age", OldValue: "Int", NewValue: "String"},
			want:   []string{"-- REVIEW REQUIRED: reverting type change", `ALTER TABLE "users" ALTER COLUMN "age" TYPE INTEGER;`},
		},
		{
			name:   "alter nullability",
			change: Change{Type: AlterNull, Model: "users", Field: "bio", OldValue: "optional", NewValue: "required"},
			want:   []string{"-- REVIEW REQUIRED: reverting nullability change", `ALTER TABLE "users" ALTER COLUMN "bio" DROP NOT NULL;`},
		},
		{
			name:   "alter default",
			change: Change{Type: AlterDefault, Model: "users", Field: "age", OldValue: "", NewValue: "5"},
			want:   []string{`ALTER TABLE "users" ALTER COLUMN "age" DROP DEFAULT;`},
		},
		{
			name:   "add index",
			change: Change{Type: AddIndex, Model: "users", NewValue: "idx_users_email", Details: map[string]string{"fields": "email"}},
			want:   []string{`DROP INDEX IF EXISTS "idx_users_email";`},
		},
		{
			name:   "drop index",
			change: Change{Type: DropIndex, Model: "users", OldValue: "idx_users_email", Details: map[string]string{"fields": "email", "unique": "false"}},
			want:   []string{`CREATE INDEX "idx_users_email" ON "users" ("email");`},
		},
		{
			name:   "add unique",
			change: Change{Type: AddUnique, Model: "users", NewValue: "uq_users_email", Details: map[string]string{"fields": "email"}},
			want:   []string{`ALTER TABLE "users" DROP CONSTRAINT IF EXISTS "uq_users_email";`},
		},
		{
			name:   "drop unique",
			change: Change{Type: DropUnique, Model: "users", OldValue: "uq_users_email", Details: map[string]string{"fields": "email"}},
			want:   []string{`ALTER TABLE "users" ADD CONSTRAINT "uq_users_email" UNIQUE ("email");`},
		},
		{
			name:   "change primary key",
			change: Change{Type: ChangePK, Model: "users", OldValue: "id", NewValue: "uid"},
			want: []string{
				`ALTER TABLE "users" DROP CONSTRAINT IF EXISTS "users_pkey";`,
				`ALTER TABLE "users" ADD PRIMARY KEY ("id");`,
			},
		},
		{
			name:   "add foreign key",
			change: Change{Type: AddFK, Model: "posts", Details: map[string]string{"fields": "author_id", "references": "id", "toModel": "users"}},
			want:   []string{`ALTER TABLE "posts" DROP CONSTRAINT IF EXISTS "fk_posts_author_id";`},
		},
		{
			name:   "drop foreign key",
			change: Change{Type: DropFK, Model: "posts", Details: map[string]string{"fields": "author_id", "references": "id", "toModel": "users", "onDelete": "CASCADE"}},
			want:   []string{`ALTER TABLE "posts" ADD CONSTRAINT "fk_posts_author_id" FOREIGN KEY ("author_id") REFERENCES "users" ("id") ON DELETE CASCADE;`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DDLGenerator{Dialect: "postgresql"}.changeToDown(tt.change, &Changeset{})
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("changeToDown() = %q, want substring %q", got, want)
				}
			}
		})
	}
}

func TestDDLGenerateEmptyChangesets(t *testing.T) {
	gen := DDLGenerator{Dialect: "postgresql"}
	if got := gen.GenerateUp(nil); got != "" {
		t.Fatalf("GenerateUp(nil) = %q, want empty", got)
	}
	if got := gen.GenerateDown(nil); got != "" {
		t.Fatalf("GenerateDown(nil) = %q, want empty", got)
	}
	if got := gen.GenerateUp(&Changeset{}); got != "" {
		t.Fatalf("GenerateUp(empty) = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Type mapping
// ---------------------------------------------------------------------------

func TestDDLNativeTypeSQL(t *testing.T) {
	gen := DDLGenerator{Dialect: "postgresql"}
	tests := []struct {
		name string
		nt   *ir.NativeType
		want string
	}{
		{"nil", nil, "TEXT"},
		{"no args", &ir.NativeType{Name: "VarChar"}, "VARCHAR"},
		{"with args", &ir.NativeType{Name: "VarChar", Args: []string{"100"}}, "VARCHAR(100)"},
		{"multi args", &ir.NativeType{Name: "Decimal", Args: []string{"10", "2"}}, "DECIMAL(10,2)"},
		{"unsafe name", &ir.NativeType{Name: "Var Char"}, "TEXT"},
		{"unsafe arg", &ir.NativeType{Name: "VarChar", Args: []string{"1);DROP"}}, "TEXT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gen.nativeTypeSQL(tt.nt); got != tt.want {
				t.Fatalf("nativeTypeSQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDDLColumnSQLType(t *testing.T) {
	enumField := &ir.Field{Name: "role", Type: ir.FieldKindEnum, EnumType: "Role"}
	schema := &ir.Schema{
		Enums: []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}, {Name: "ADMIN"}}}},
	}
	listField := testField("tags", "Int")
	listField.IsList = true

	tests := []struct {
		name    string
		dialect string
		schema  *ir.Schema
		field   *ir.Field
		want    string
	}{
		{"nil field", "postgresql", nil, nil, "TEXT"},
		{"native type", "postgresql", nil, &ir.Field{Name: "name", Type: ir.FieldKindScalar, ScalarType: "String", NativeType: &ir.NativeType{Name: "VarChar", Args: []string{"100"}}}, "VARCHAR(100)"},
		{"postgres enum", "postgresql", schema, enumField, `"Role"`},
		{"mysql enum", "mysql", schema, enumField, "ENUM('USER', 'ADMIN')"},
		{"mysql unknown enum", "mysql", nil, enumField, "TEXT"},
		{"sqlite enum", "sqlite", schema, enumField, "TEXT"},
		{"postgres list", "postgresql", nil, listField, "INTEGER[]"},
		{"mysql list", "mysql", nil, listField, "JSON"},
		{"sqlite list", "sqlite", nil, listField, "TEXT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := DDLGenerator{Dialect: tt.dialect, Schema: tt.schema}
			if got := gen.columnSQLType(tt.field); got != tt.want {
				t.Fatalf("columnSQLType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindEnum(t *testing.T) {
	schema := &ir.Schema{Enums: []*ir.Enum{{Name: "Role"}}}
	if got := findEnum(nil, "Role"); got != nil {
		t.Fatalf("findEnum(nil) = %v, want nil", got)
	}
	if got := findEnum(schema, "Missing"); got != nil {
		t.Fatalf("findEnum(missing) = %v, want nil", got)
	}
	if got := findEnum(schema, "Role"); got == nil || got.Name != "Role" {
		t.Fatalf("findEnum(Role) = %v", got)
	}
}

// ---------------------------------------------------------------------------
// Misc DDL helpers
// ---------------------------------------------------------------------------

func TestDDLAutoIncrementClauseAndDialect(t *testing.T) {
	tests := []struct {
		dialect string
		want    string
	}{
		{"postgresql", "GENERATED BY DEFAULT AS IDENTITY"},
		{"mysql", "AUTO_INCREMENT"},
		{"sqlite", "AUTOINCREMENT"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.dialect, func(t *testing.T) {
			gen := DDLGenerator{Dialect: tt.dialect}
			if got := gen.autoIncrementClause(); got != tt.want {
				t.Fatalf("autoIncrementClause() = %q, want %q", got, tt.want)
			}
			if tt.dialect == "unknown" {
				if gen.sqlDialect() != nil {
					t.Fatal("sqlDialect() for unknown dialect should be nil")
				}
				if got := gen.defaultExpr("uuid()"); got != "uuid()" {
					t.Fatalf("defaultExpr() without dialect mapping = %q, want passthrough", got)
				}
			}
		})
	}
}

func TestDDLDefaultExprForFieldFallbacks(t *testing.T) {
	gen := DDLGenerator{Dialect: "postgresql"}
	if got := gen.defaultExprForField(nil); got != "''" {
		t.Fatalf("defaultExprForField(nil) = %q, want ''", got)
	}
	if got := gen.defaultExprForField(testField("name", "String")); got != "''" {
		t.Fatalf("defaultExprForField(no default) = %q, want ''", got)
	}
}

func TestDDLCollationSQL(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
		input   string
		want    string
	}{
		{"empty", "postgresql", "", ""},
		{"postgres qualified", "postgresql", "pg_catalog.default", `"pg_catalog"."default"`},
		{"mysql passthrough", "mysql", "utf8mb4_bin", "utf8mb4_bin"},
		{"quoted passthrough", "postgresql", `"custom"`, `"custom"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (DDLGenerator{Dialect: tt.dialect}).collationSQL(tt.input); got != tt.want {
				t.Fatalf("collationSQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDDLChangePKSQLMySQL(t *testing.T) {
	change := Change{Type: ChangePK, Model: "users", OldValue: "id", NewValue: "uid"}
	got := DDLGenerator{Dialect: "mysql"}.changePKSQL(change)
	if !strings.Contains(got, "ALTER TABLE `users` DROP PRIMARY KEY;") {
		t.Fatalf("mysql change PK SQL missing DROP PRIMARY KEY:\n%s", got)
	}
	if !strings.Contains(got, "ALTER TABLE `users` ADD PRIMARY KEY (`uid`);") {
		t.Fatalf("mysql change PK SQL missing ADD PRIMARY KEY:\n%s", got)
	}
}

func TestDDLDropUniqueNamedByDialect(t *testing.T) {
	change := Change{Type: DropUnique, Model: "users", OldValue: "uq_users_email", Details: map[string]string{"fields": "email"}}

	mysql := DDLGenerator{Dialect: "mysql"}.dropUniqueSQL(change)
	if mysql != "DROP INDEX `uq_users_email` ON `users`;" {
		t.Fatalf("mysql drop unique SQL = %q", mysql)
	}
	sqlite := DDLGenerator{Dialect: "sqlite"}.dropUniqueSQL(change)
	if sqlite != `DROP INDEX IF EXISTS "uq_users_email";` {
		t.Fatalf("sqlite drop unique SQL = %q", sqlite)
	}
}

func TestModelUniqueConstraints(t *testing.T) {
	if got := modelUniqueConstraints(nil); got != nil {
		t.Fatalf("modelUniqueConstraints(nil) = %v, want nil", got)
	}

	idField := testField("id", "Int")
	idField.IsID = true
	idField.IsUnique = true
	email := testField("email", "String")
	email.IsUnique = true
	model := testModel("User", idField, email)
	model.UniqueConstraints = []*ir.UniqueConstraint{
		{Name: "uq_email", Fields: []string{"email"}},
		{Fields: []string{"email"}}, // duplicate key, must be deduplicated
	}

	got := modelUniqueConstraints(model)
	if len(got) != 1 {
		t.Fatalf("modelUniqueConstraints() = %v, want one deduplicated constraint", got)
	}
	if got[0].Name != "uq_email" {
		t.Fatalf("constraint name = %q, want uq_email", got[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Regression tests for code review findings
// ---------------------------------------------------------------------------

func TestDDLSQLiteRebuildTableWithPreexistingIndex(t *testing.T) {
	oldUser := testModel("User", testField("id", "Int"), testField("name", "String"), testField("age", "Int"))
	newUser := testModel("User", testField("id", "Int"), testField("name", "String"), testField("age", "String"))
	newUser.Indexes = []*ir.Index{{Name: "idx_users_name", Fields: []string{"name"}}}

	cs := Diff(testSchema(oldUser), testSchema(newUser))
	up := DDLGenerator{Dialect: "sqlite", Schema: cs.New}.GenerateUp(cs)

	// The rebuild must emit the index exactly once: the temporary CREATE TABLE
	// must not carry the original table's index statements.
	if count := strings.Count(up, `CREATE INDEX "idx_users_name"`); count != 1 {
		t.Fatalf("rebuild SQL creates index %d times, want 1:\n%s", count, up)
	}

	// Apply against a database where the old table already has the index, as
	// created by an earlier migration.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, stmt := range []string{
		`CREATE TABLE "User" (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`,
		`CREATE INDEX "idx_users_name" ON "User" ("name")`,
		`INSERT INTO "User" (name, age) VALUES ('alice', 30)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if _, err := db.Exec(up); err != nil {
		t.Fatalf("apply rebuild SQL: %v\n%s", err, up)
	}

	var name, age string
	if err := db.QueryRow(`SELECT name, age FROM "User"`).Scan(&name, &age); err != nil {
		t.Fatalf("query rebuilt table: %v", err)
	}
	if name != "alice" || age != "30" {
		t.Fatalf("rebuilt row = (%q, %q), want (alice, 30)", name, age)
	}
	var indexName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_users_name'`).Scan(&indexName); err != nil {
		t.Fatalf("recreated index missing: %v", err)
	}
}

func TestDDLChangeToDownSwapsFieldDefinitions(t *testing.T) {
	oldField := testField("age", "Int")
	newField := testField("age", "BigInt")

	tests := []struct {
		name    string
		dialect string
		change  Change
		want    string
	}{
		{
			name:    "mysql alter type uses old field definition",
			dialect: "mysql",
			change:  Change{Type: AlterType, Model: "users", Field: "age", OldValue: "Int", NewValue: "BigInt", OldField: oldField, NewField: newField},
			want:    "ALTER TABLE `users` MODIFY COLUMN `age` INT NOT NULL;",
		},
		{
			name:    "postgres alter type uses old field definition",
			dialect: "postgresql",
			change:  Change{Type: AlterType, Model: "users", Field: "age", OldValue: "Int", NewValue: "BigInt", OldField: oldField, NewField: newField},
			want:    `ALTER TABLE "users" ALTER COLUMN "age" TYPE INTEGER;`,
		},
		{
			name:    "mysql alter null restores optionality",
			dialect: "mysql",
			change: Change{Type: AlterNull, Model: "users", Field: "age", OldValue: "optional", NewValue: "required",
				OldField: testFieldOptional("age", "Int"), NewField: newField},
			want: "ALTER TABLE `users` MODIFY COLUMN `age` INT;",
		},
		{
			name:    "postgres alter default restores old value",
			dialect: "postgresql",
			change: Change{Type: AlterDefault, Model: "users", Field: "age", OldValue: "1", NewValue: "5",
				OldField: testFieldWithDefault("age", "Int", &ir.DefaultValue{Value: "1"}),
				NewField: testFieldWithDefault("age", "Int", &ir.DefaultValue{Value: "5"})},
			want: `ALTER TABLE "users" ALTER COLUMN "age" SET DEFAULT 1;`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DDLGenerator{Dialect: tt.dialect}.changeToDown(tt.change, &Changeset{})
			if !strings.Contains(got, tt.want) {
				t.Fatalf("changeToDown() = %q, want substring %q", got, tt.want)
			}
		})
	}
}

func TestDDLPostgresEnumTypeUsesDBName(t *testing.T) {
	enum := &ir.Enum{Name: "Status", DBName: "task_status", Values: []*ir.EnumValue{{Name: "OPEN", DBName: "open"}}}
	statusField := &ir.Field{Name: "status", Type: ir.FieldKindEnum, EnumType: "Status"}
	task := testModel("Task", testField("id", "Int"), statusField)
	schema := &ir.Schema{Models: []*ir.Model{task}, Enums: []*ir.Enum{enum}}
	gen := DDLGenerator{Dialect: "postgresql", Schema: schema}

	if got := gen.columnSQLType(statusField); got != `"task_status"` {
		t.Fatalf("columnSQLType() = %q, want %q", got, `"task_status"`)
	}

	up := gen.GenerateUp(Diff(nil, schema))
	for _, want := range []string{
		`CREATE TYPE "task_status" AS ENUM ('open');`,
		`"status" "task_status" NOT NULL`,
	} {
		if !strings.Contains(up, want) {
			t.Fatalf("postgres enum SQL missing %q:\n%s", want, up)
		}
	}

	// Dropping the enum must target the mapped database type name as well.
	got := gen.changeToUp(Change{Type: DropEnum, Model: "Status"}, &Changeset{Old: schema})
	if got != `DROP TYPE "task_status";` {
		t.Fatalf("drop enum SQL = %q, want DROP TYPE on mapped name", got)
	}
}

func TestDDLDefaultExprEscapesSingleQuotes(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
		got     func(DDLGenerator) string
		want    string
	}{
		{
			name:    "string default",
			dialect: "postgresql",
			got:     func(g DDLGenerator) string { return g.defaultExpr("O'Reilly") },
			want:    `'O''Reilly'`,
		},
		{
			name:    "postgres array element",
			dialect: "postgresql",
			got: func(g DDLGenerator) string {
				return g.arrayDefaultExpr(&ir.DefaultValue{IsArray: true, ArrayValue: []string{"O'Reilly", "x"}})
			},
			want: `ARRAY['O''Reilly', 'x']`,
		},
		{
			name:    "sqlite array literal",
			dialect: "sqlite",
			got: func(g DDLGenerator) string {
				return g.arrayDefaultExpr(&ir.DefaultValue{IsArray: true, ArrayValue: []string{"O'Reilly", "x"}})
			},
			want: `'[O''Reilly,x]'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got(DDLGenerator{Dialect: tt.dialect}); got != tt.want {
				t.Fatalf("default expression = %q, want %q", got, tt.want)
			}
		})
	}

	// End-to-end: reverting a default change must produce valid SQL for a
	// legacy default containing a single quote.
	oldUser := testModel("User", testField("id", "Int"),
		testFieldWithDefault("name", "String", &ir.DefaultValue{Value: "O'Reilly", IsString: true}))
	newUser := testModel("User", testField("id", "Int"),
		testFieldWithDefault("name", "String", &ir.DefaultValue{Value: "unknown", IsString: true}))

	cs := Diff(testSchema(oldUser), testSchema(newUser))
	down := DDLGenerator{Dialect: "postgresql", Schema: cs.New}.GenerateDown(cs)
	if !strings.Contains(down, `SET DEFAULT 'O''Reilly';`) {
		t.Fatalf("down SQL missing escaped old default:\n%s", down)
	}
}

func TestDDLAddFKSQLUsesExplicitConstraintName(t *testing.T) {
	details := map[string]string{"fields": "author_id", "references": "id", "toModel": "users", "name": "fk_posts_author"}
	gen := DDLGenerator{Dialect: "postgresql"}

	got := gen.addFKSQL(Change{Type: AddFK, Model: "posts", Details: details})
	want := `ALTER TABLE "posts" ADD CONSTRAINT "fk_posts_author" FOREIGN KEY ("author_id") REFERENCES "users" ("id");`
	if got != want {
		t.Fatalf("addFKSQL() = %q, want %q", got, want)
	}

	// Without an explicit name the generated name is used.
	delete(details, "name")
	got = gen.addFKSQL(Change{Type: AddFK, Model: "posts", Details: details})
	want = `ALTER TABLE "posts" ADD CONSTRAINT "fk_posts_author_id" FOREIGN KEY ("author_id") REFERENCES "users" ("id");`
	if got != want {
		t.Fatalf("addFKSQL() = %q, want %q", got, want)
	}

	// Down of DropFK recreates the constraint under its explicit name,
	// symmetric with dropFKSQL honoring Details["name"].
	details["name"] = "fk_posts_author"
	down := gen.changeToDown(Change{Type: DropFK, Model: "posts", Details: details}, &Changeset{})
	if !strings.Contains(down, `ADD CONSTRAINT "fk_posts_author"`) {
		t.Fatalf("changeToDown(DropFK) = %q, want explicit constraint name", down)
	}
}

func TestDDLChangeToDownEnums(t *testing.T) {
	t.Run("create enum drops mapped type", func(t *testing.T) {
		cs := &Changeset{New: &ir.Schema{Enums: []*ir.Enum{
			{Name: "Status", DBName: "task_status", Values: []*ir.EnumValue{{Name: "OPEN"}}},
		}}}
		got := DDLGenerator{Dialect: "postgresql"}.changeToDown(Change{Type: CreateEnum, Model: "Status"}, cs)
		if got != `DROP TYPE IF EXISTS "task_status";` {
			t.Fatalf("changeToDown(CreateEnum) = %q", got)
		}
	})

	t.Run("create enum falls back to schema name", func(t *testing.T) {
		got := DDLGenerator{Dialect: "postgresql"}.changeToDown(Change{Type: CreateEnum, Model: "Role"}, &Changeset{})
		if got != `DROP TYPE IF EXISTS "Role";` {
			t.Fatalf("changeToDown(CreateEnum) = %q", got)
		}
	})

	t.Run("drop enum recreates from old schema", func(t *testing.T) {
		cs := &Changeset{Old: &ir.Schema{Enums: []*ir.Enum{
			{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}, {Name: "LEGACY"}}},
		}}}
		got := DDLGenerator{Dialect: "postgresql"}.changeToDown(Change{Type: DropEnum, Model: "Role"}, cs)
		for _, want := range []string{
			"DO $$ BEGIN",
			`CREATE TYPE "Role" AS ENUM ('USER', 'LEGACY');`,
			"EXCEPTION WHEN duplicate_object THEN NULL;",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("changeToDown(DropEnum) missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("drop enum without old schema warns", func(t *testing.T) {
		got := DDLGenerator{Dialect: "postgresql"}.changeToDown(Change{Type: DropEnum, Model: "Role"}, &Changeset{})
		if !strings.Contains(got, "-- WARNING") {
			t.Fatalf("changeToDown(DropEnum) = %q, want warning", got)
		}
	})

	t.Run("alter enum postgres requires review", func(t *testing.T) {
		got := DDLGenerator{Dialect: "postgresql"}.changeToDown(Change{Type: AlterEnum, Model: "Role"}, &Changeset{})
		if !strings.Contains(got, "-- REVIEW REQUIRED") {
			t.Fatalf("changeToDown(AlterEnum) = %q, want review comment", got)
		}
	})

	t.Run("alter enum mysql restores old values", func(t *testing.T) {
		roleField := &ir.Field{Name: "role", Type: ir.FieldKindEnum, EnumType: "Role"}
		cs := &Changeset{Old: &ir.Schema{
			Models: []*ir.Model{testModel("users", testField("id", "Int"), roleField)},
			Enums:  []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}, {Name: "LEGACY"}}}},
		}}
		got := DDLGenerator{Dialect: "mysql"}.changeToDown(Change{Type: AlterEnum, Model: "Role"}, cs)
		want := "ALTER TABLE `users` MODIFY COLUMN `role` ENUM('USER', 'LEGACY') NOT NULL;"
		if got != want {
			t.Fatalf("changeToDown(AlterEnum) = %q, want %q", got, want)
		}
	})

	t.Run("non-postgres create and drop enum are no-ops", func(t *testing.T) {
		for _, dialect := range []string{"mysql", "sqlite"} {
			gen := DDLGenerator{Dialect: dialect}
			if got := gen.changeToDown(Change{Type: CreateEnum, Model: "Role"}, &Changeset{}); got != "" {
				t.Fatalf("%s changeToDown(CreateEnum) = %q, want empty", dialect, got)
			}
			if got := gen.changeToDown(Change{Type: DropEnum, Model: "Role"}, &Changeset{}); got != "" {
				t.Fatalf("%s changeToDown(DropEnum) = %q, want empty", dialect, got)
			}
		}
	})
}

func TestDDLPostgresCreateEnumIsGuarded(t *testing.T) {
	schema := &ir.Schema{Enums: []*ir.Enum{
		{Name: "Status", Values: []*ir.EnumValue{{Name: "OPEN"}}},
	}}
	up := DDLGenerator{Dialect: "postgresql", Schema: schema}.GenerateUp(Diff(nil, schema))

	for _, want := range []string{
		"DO $$ BEGIN",
		`CREATE TYPE "Status" AS ENUM ('OPEN');`,
		"EXCEPTION WHEN duplicate_object THEN NULL;",
		"END $$;",
	} {
		if !strings.Contains(up, want) {
			t.Fatalf("guarded CREATE TYPE missing %q:\n%s", want, up)
		}
	}
}

func TestDDLEnumDependencyOrder(t *testing.T) {
	plain := testSchema(testModel("users", testField("id", "Int"), testField("role", "String")))
	withEnum := testSchema(testModel("users", testField("id", "Int"), &ir.Field{Name: "role", Type: ir.FieldKindEnum, EnumType: "Role"}))
	withEnum.Enums = []*ir.Enum{{Name: "Role", DBName: "roles", Values: []*ir.EnumValue{{Name: "USER"}}}}
	gen := DDLGenerator{Dialect: "postgresql", Schema: withEnum}
	down := gen.GenerateDown(Diff(plain, withEnum))
	alter := strings.Index(down, `ALTER TABLE "users" ALTER COLUMN "role" TYPE TEXT`)
	drop := strings.Index(down, `DROP TYPE IF EXISTS "roles"`)
	if alter < 0 || drop < 0 || alter > drop {
		t.Fatalf("rollback must detach columns before dropping their enum:\n%s", down)
	}

	gen.Schema = plain
	down = gen.GenerateDown(Diff(withEnum, plain))
	create := strings.Index(down, `CREATE TYPE "roles"`)
	alter = strings.Index(down, `ALTER TABLE "users" ALTER COLUMN "role" TYPE "roles"`)
	if create < 0 || alter < 0 || create > alter {
		t.Fatalf("rollback must recreate the mapped enum before restoring its column:\n%s", down)
	}

	up := gen.GenerateUp(Diff(withEnum, nil))
	if strings.Index(up, `DROP TABLE "users"`) > strings.Index(up, `DROP TYPE "roles"`) {
		t.Fatalf("teardown must drop tables before their enum types:\n%s", up)
	}
}

func TestDDLPostgresEnumDelimiterCannotOccurInBody(t *testing.T) {
	enum := &ir.Enum{Name: "Role", DBName: "role$$type", Values: []*ir.EnumValue{{Name: "USER", DBName: "value$$with$gco$tags"}}}
	sql := (DDLGenerator{Dialect: "postgresql"}).postgresCreateEnumSQL(enum)
	words := strings.Fields(sql)
	if len(words) < 2 || strings.Count(sql, words[1]) != 2 {
		t.Fatalf("DO block delimiter must occur only at its boundaries:\n%s", sql)
	}
}

func TestDDLMySQLRollbackKeepsOldEnumValues(t *testing.T) {
	oldField := &ir.Field{Name: "role", Type: ir.FieldKindEnum, EnumType: "Role", IsOptional: true}
	newField := *oldField
	newField.IsOptional = false
	old := testSchema(testModel("users", testField("id", "Int"), oldField))
	old.Enums = []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}}}}
	newSchema := testSchema(testModel("users", testField("id", "Int"), &newField))
	newSchema.Enums = []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}, {Name: "ADMIN"}}}}
	down := (DDLGenerator{Dialect: "mysql", Schema: newSchema}).GenerateDown(Diff(old, newSchema))
	if strings.Contains(down, "'ADMIN'") || !strings.Contains(down, "ENUM('USER')") {
		t.Fatalf("rollback must not reintroduce new enum values when restoring nullability:\n%s", down)
	}
}

func TestDDLSQLiteRebuildWithStatementMarkerInDefault(t *testing.T) {
	oldField := testField("label", "String")
	newField := *oldField
	newField.Default = &ir.DefaultValue{Value: "first\n);second", IsString: true}
	old := testSchema(testModel("users", testField("id", "Int"), oldField))
	newSchema := testSchema(testModel("users", testField("id", "Int"), &newField))
	gen := DDLGenerator{Dialect: "sqlite", Schema: newSchema}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, label TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	up := gen.GenerateUp(Diff(old, newSchema))
	if _, err := db.Exec(up); err != nil {
		t.Fatalf("rebuild failed: %v\n%s", err, up)
	}
	if _, err := db.Exec(`INSERT INTO users (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT label FROM users WHERE id = 1`).Scan(&got); err != nil || got != newField.Default.Value {
		t.Fatalf("default after rebuild = %q, error=%v", got, err)
	}
}

func TestDDLMySQLModifyColumnOmitsInlineUnique(t *testing.T) {
	uniqueEmail := testField("email", "String")
	uniqueEmail.IsUnique = true
	gen := DDLGenerator{Dialect: "mysql"}

	t.Run("alter type", func(t *testing.T) {
		got := gen.alterTypeSQL(Change{Type: AlterType, Model: "users", Field: "email", NewValue: "String", NewField: uniqueEmail})
		if strings.Contains(got, "UNIQUE") {
			t.Fatalf("alterTypeSQL() = %q, must not inline UNIQUE", got)
		}
	})

	t.Run("alter null", func(t *testing.T) {
		got := gen.alterNullSQL(Change{Type: AlterNull, Model: "users", Field: "email", NewValue: "required", NewField: uniqueEmail})
		if strings.Contains(got, "UNIQUE") {
			t.Fatalf("alterNullSQL() = %q, must not inline UNIQUE", got)
		}
	})

	t.Run("enum refresh", func(t *testing.T) {
		uniqueRole := &ir.Field{Name: "role", Type: ir.FieldKindEnum, EnumType: "Role", IsUnique: true}
		schema := &ir.Schema{
			Models: []*ir.Model{testModel("users", testField("id", "Int"), uniqueRole)},
			Enums:  []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}}}},
		}
		got := (DDLGenerator{Dialect: "mysql", Schema: schema}).mysqlRefreshEnumColumns("Role")
		if strings.Contains(got, "UNIQUE") {
			t.Fatalf("mysqlRefreshEnumColumns() = %q, must not inline UNIQUE", got)
		}
	})

	t.Run("add column keeps inline unique", func(t *testing.T) {
		cs := &Changeset{New: testSchema(testModel("users", testField("id", "Int"), uniqueEmail))}
		got := gen.addColumnSQL(Change{Type: AddColumn, Model: "users", Field: "email"}, cs)
		if !strings.Contains(got, "UNIQUE") {
			t.Fatalf("addColumnSQL() = %q, want inline UNIQUE for new unique column", got)
		}
	})
}
