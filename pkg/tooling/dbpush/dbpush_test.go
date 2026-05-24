package dbpush

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
)

func TestResolveURLUsesSchemaDatasourceURL(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URL:      "postgresql://postgres:secret@localhost:15432/postgres?schema=public",
		},
		Models: []*ir.Model{{Name: "User"}},
	}

	got, source, err := resolveURL("", schema)
	if err != nil {
		t.Fatalf("resolveURL() error = %v", err)
	}
	if got == "" {
		t.Fatal("resolveURL() returned empty URL")
	}
	parsedURL, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsedURL.Query().Get("schema") != "" {
		t.Fatalf("resolveURL() kept schema query param: %q", got)
	}
	if parsedURL.Query().Get("search_path") != "public" {
		t.Fatalf("resolveURL() search_path = %q, want %q", parsedURL.Query().Get("search_path"), "public")
	}
	if parsedURL.Query().Get("sslmode") != "disable" {
		t.Fatalf("resolveURL() sslmode = %q, want %q", parsedURL.Query().Get("sslmode"), "disable")
	}
	if source != "schema datasource" {
		t.Fatalf("resolveURL() source = %q, want %q", source, "schema datasource")
	}
}

func TestResolveURLUsesDatasourceEnvURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://postgres:secret@localhost:15432/postgres?schema=public")
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URLIsEnv: true,
			EnvVar:   "DATABASE_URL",
		},
		Models: []*ir.Model{{Name: "User"}},
	}

	got, source, err := resolveURL("", schema)
	if err != nil {
		t.Fatalf("resolveURL() error = %v", err)
	}
	if got == "" {
		t.Fatal("resolveURL() returned empty URL")
	}
	parsedURL, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsedURL.Query().Get("search_path") != "public" {
		t.Fatalf("resolveURL() search_path = %q, want %q", parsedURL.Query().Get("search_path"), "public")
	}
	if !strings.Contains(source, `env("DATABASE_URL")`) {
		t.Fatalf("resolveURL() source = %q", source)
	}
}

func TestResolveURLPreservesExistingSearchPathAndSSLMode(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URL:      "postgresql://postgres:secret@db.example.com:5432/postgres?schema=tenant&search_path=custom&sslmode=require",
		},
	}

	got, _, err := resolveURL("", schema)
	if err != nil {
		t.Fatalf("resolveURL() error = %v", err)
	}
	parsedURL, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsedURL.Query().Get("schema") != "" {
		t.Fatalf("resolveURL() kept schema query param: %q", got)
	}
	if parsedURL.Query().Get("search_path") != "custom" {
		t.Fatalf("resolveURL() search_path = %q, want %q", parsedURL.Query().Get("search_path"), "custom")
	}
	if parsedURL.Query().Get("sslmode") != "require" {
		t.Fatalf("resolveURL() sslmode = %q, want %q", parsedURL.Query().Get("sslmode"), "require")
	}
}

func TestNormalizeMySQLURLConvertsURLFormToDSN(t *testing.T) {
	got, err := normalizeConnectionURL("mysql://user:secret@localhost:3306/app?parseTime=true", "mysql")
	if err != nil {
		t.Fatalf("normalizeConnectionURL() error = %v", err)
	}
	want := "user:secret@tcp(localhost:3306)/app?parseTime=true"
	if got != want {
		t.Fatalf("normalizeConnectionURL() = %q, want %q", got, want)
	}
}

func TestNormalizeMySQLURLPreservesDriverDSN(t *testing.T) {
	dsn := "user:secret@tcp(localhost:3306)/app?parseTime=true"
	got, err := normalizeConnectionURL(dsn, "mysql")
	if err != nil {
		t.Fatalf("normalizeConnectionURL() error = %v", err)
	}
	if got != dsn {
		t.Fatalf("normalizeConnectionURL() = %q, want %q", got, dsn)
	}
}

func TestNormalizeSQLiteURL(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"file:./dev.db", "file:./dev.db"},
		{":memory:", ":memory:"},
		{"sqlite:///tmp/app.db", "/tmp/app.db"},
	}

	for _, tc := range cases {
		got, err := normalizeConnectionURL(tc.raw, "sqlite")
		if err != nil {
			t.Fatalf("normalizeConnectionURL(%q) error = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeConnectionURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestProviderDriverMapping(t *testing.T) {
	if !isSupportedProvider("mysql") || !isSupportedProvider("sqlite") {
		t.Fatal("mysql and sqlite should be supported providers")
	}
	if driverName("postgresql") != "pgx" {
		t.Fatalf("postgresql driver = %q", driverName("postgresql"))
	}
	if driverName("mysql") != "mysql" {
		t.Fatalf("mysql driver = %q", driverName("mysql"))
	}
	if driverName("sqlite") != "sqlite" {
		t.Fatalf("sqlite driver = %q", driverName("sqlite"))
	}
}

func TestResolveSchemaNameUsesDatasourceSchemaFirst(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{Schema: "app"},
	}

	got := resolveSchemaName(schema, "postgresql://postgres:secret@localhost:15432/postgres?search_path=public")
	if got != "app" {
		t.Fatalf("resolveSchemaName() = %q, want %q", got, "app")
	}
}

func TestResolveSchemaNameUsesSearchPathFromURL(t *testing.T) {
	got := resolveSchemaName(nil, "postgresql://postgres:secret@localhost:15432/postgres?search_path=tenant,public")
	if got != "tenant" {
		t.Fatalf("resolveSchemaName() = %q, want %q", got, "tenant")
	}
}

func TestResolveURLAllowsURLFlagOverride(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URLIsEnv: true,
			EnvVar:   "DATABASE_URL",
		},
	}

	got, source, err := resolveURL("postgresql://override", schema)
	if err != nil {
		t.Fatalf("resolveURL() error = %v", err)
	}
	if got != "postgresql://override" {
		t.Fatalf("resolveURL() = %q", got)
	}
	if source != "--url" {
		t.Fatalf("resolveURL() source = %q, want %q", source, "--url")
	}
}

func TestResolveURLReturnsHelpfulErrorWithoutURL(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URLIsEnv: true,
			EnvVar:   "DATABASE_URL",
		},
		Models: []*ir.Model{{Name: "User"}},
	}

	errURL, _, err := resolveURL("", schema)
	if err == nil {
		t.Fatal("resolveURL() error = nil, want error")
	}
	if errURL != "" {
		t.Fatalf("resolveURL() URL = %q, want empty", errURL)
	}
	if !strings.Contains(err.Error(), `env("DATABASE_URL")`) {
		t.Fatalf("resolveURL() error = %v", err)
	}
}

func TestSplitStatementsSkipsComments(t *testing.T) {
	stmts, unsupported := splitStatements(`
CREATE TABLE "users" ("id" INTEGER NOT NULL);
-- SQLite: unsupported
ALTER TABLE "users" ADD COLUMN "name" TEXT;
`)
	if len(stmts) != 2 {
		t.Fatalf("len(stmts) = %d, want 2", len(stmts))
	}
	if len(unsupported) != 1 {
		t.Fatalf("len(unsupported) = %d, want 1", len(unsupported))
	}
}

func TestRiskyChanges(t *testing.T) {
	cs := &migrate.Changeset{
		Changes: []migrate.Change{
			{Type: migrate.AddColumn, Model: "users", Field: "name", Rollback: migrate.SafeRollback},
			{Type: migrate.DropColumn, Model: "users", Field: "legacy", Rollback: migrate.DestructiveRollback},
			{Type: migrate.AlterType, Model: "users", Field: "age", Rollback: migrate.ReviewRequired},
		},
	}

	got := riskyChanges(cs)
	if len(got) != 2 {
		t.Fatalf("len(riskyChanges) = %d, want 2", len(got))
	}
}

func TestDatabaseScalarTypeSmallInt(t *testing.T) {
	if got := postgresScalarType("smallint", "int2"); got != "SmallInt" {
		t.Fatalf("postgresScalarType(smallint) = %q, want SmallInt", got)
	}
	if got := mysqlScalarType("smallint", "smallint"); got != "SmallInt" {
		t.Fatalf("mysqlScalarType(smallint) = %q, want SmallInt", got)
	}
	if got := sqliteScalarType("SMALLINT"); got != "SmallInt" {
		t.Fatalf("sqliteScalarType(SMALLINT) = %q, want SmallInt", got)
	}
}

func TestPostgresColumnTypeArray(t *testing.T) {
	scalarType, isList := postgresColumnType("ARRAY", "_int8")
	if scalarType != "BigInt" || !isList {
		t.Fatalf("postgresColumnType(ARRAY, _int8) = %q, %v; want BigInt, true", scalarType, isList)
	}

	scalarType, isList = postgresColumnType("ARRAY", "_int2")
	if scalarType != "SmallInt" || !isList {
		t.Fatalf("postgresColumnType(ARRAY, _int2) = %q, %v; want SmallInt, true", scalarType, isList)
	}
}

func TestPostgresDefaultValueNormalizesIdentityAndCasts(t *testing.T) {
	identity := postgresDefaultValue("", true)
	if identity == nil || !identity.IsFunction || identity.FuncName != "autoincrement" {
		t.Fatalf("identity default = %#v, want autoincrement", identity)
	}

	text := postgresDefaultValue("''::text", false)
	if text == nil || !text.IsString || text.Value != "" {
		t.Fatalf("text default = %#v, want empty string", text)
	}

	json := postgresDefaultValue("'[]'::jsonb", false)
	if json == nil || !json.IsString || json.Value != "[]" {
		t.Fatalf("json default = %#v, want [] string", json)
	}

	number := postgresDefaultValue("0", false)
	if number == nil || !number.IsNumber || number.Value != "0" {
		t.Fatalf("numeric default = %#v, want numeric 0", number)
	}

	array := postgresDefaultValue("'{}'::bigint[]", false)
	if array == nil || !array.IsArray || len(array.ArrayValue) != 0 {
		t.Fatalf("array default = %#v, want empty array", array)
	}
}

func TestParsePostgresIndexColumnDef(t *testing.T) {
	col := parsePostgresIndexColumnDef("published_at", `"published_at" COLLATE "pg_catalog"."default" timestamptz_ops ASC NULLS LAST`)

	if col.Field != "published_at" {
		t.Fatalf("field = %q", col.Field)
	}
	if col.Collation != "pg_catalog.default" {
		t.Fatalf("collation = %q", col.Collation)
	}
	if col.OpClass != "timestamptz_ops" {
		t.Fatalf("opclass = %q", col.OpClass)
	}
	if col.Sort != "ASC" {
		t.Fatalf("sort = %q", col.Sort)
	}
	if col.Nulls != "LAST" {
		t.Fatalf("nulls = %q", col.Nulls)
	}
}

func TestParsePostgresIndexColumns(t *testing.T) {
	cols := parsePostgresIndexColumns(
		[]string{"status", "published_at"},
		[]string{`status int8_ops DESC`, `"published_at" timestamptz_ops ASC NULLS LAST`},
		nil,
		nil,
		nil,
	)

	if len(cols) != 2 {
		t.Fatalf("columns = %d, want 2", len(cols))
	}
	if cols[0].Field != "status" || cols[0].OpClass != "int8_ops" || cols[0].Sort != "DESC" {
		t.Fatalf("first column = %+v", cols[0])
	}
	if cols[1].Field != "published_at" || cols[1].OpClass != "timestamptz_ops" || cols[1].Sort != "ASC" || cols[1].Nulls != "LAST" {
		t.Fatalf("second column = %+v", cols[1])
	}
}

func TestParsePostgresIndexColumnsCatalogOptions(t *testing.T) {
	cols := parsePostgresIndexColumns(
		[]string{"pinned", "published_at", "id"},
		[]string{"pinned", "published_at", "id"},
		[]string{"1", "1", "1"},
		[]string{"bool_ops", "timestamptz_ops", "int8_ops"},
		[]string{"NULL", "NULL", "NULL"},
	)

	if len(cols) != 3 {
		t.Fatalf("columns = %d, want 3", len(cols))
	}
	for _, col := range cols {
		if col.Sort != "DESC" || col.Nulls != "LAST" {
			t.Fatalf("column options = %+v, want DESC NULLS LAST", col)
		}
	}
	if cols[0].OpClass != "bool_ops" || cols[1].OpClass != "timestamptz_ops" || cols[2].OpClass != "int8_ops" {
		t.Fatalf("opclasses = %+v", cols)
	}
}

func TestIntrospectSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			name TEXT,
			created_at TEXT DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE posts (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			author_id INTEGER NOT NULL,
			FOREIGN KEY(author_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX idx_posts_author_id ON posts(author_id)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	schema, err := introspectSQLite(context.Background(), db)
	if err != nil {
		t.Fatalf("introspectSQLite() error = %v", err)
	}
	if schema.Datasource.Provider != "sqlite" {
		t.Fatalf("provider = %q, want sqlite", schema.Datasource.Provider)
	}

	users := findTestModel(schema, "users")
	if users == nil {
		t.Fatal("users model not found")
	}
	email := findFieldByColumn(users, "email")
	if email == nil || !email.IsUnique || email.IsOptional {
		t.Fatalf("email field = %#v", email)
	}
	createdAt := findFieldByColumn(users, "created_at")
	if createdAt == nil || createdAt.Default == nil || createdAt.Default.FuncName != "now" {
		t.Fatalf("created_at default = %#v", createdAt)
	}

	posts := findTestModel(schema, "posts")
	if posts == nil {
		t.Fatal("posts model not found")
	}
	if len(posts.Indexes) != 1 || posts.Indexes[0].Name != "idx_posts_author_id" {
		t.Fatalf("posts indexes = %#v", posts.Indexes)
	}
	if len(posts.Relations) != 1 || posts.Relations[0].ToModel != "users" || posts.Relations[0].OnDelete != "CASCADE" {
		t.Fatalf("posts relations = %#v", posts.Relations)
	}
}

func TestRunSQLiteDBPushCreatesTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dev.db")
	schemaPath := filepath.Join(dir, "schema.gcorm")
	schema := `datasource db {
  provider = "sqlite"
  url      = "file:` + filepath.ToSlash(dbPath) + `"
}

generator client {
  provider = "gco-go"
  output   = "./gen"
}

model User {
  id    String @id
  email String @unique
  name  String?
  posts Post[]
}

model Post {
  id       String @id
  title    String
  authorId String
  author   User   @relation(fields: [authorId], references: [id], onDelete: CASCADE)
}
`
	if err := os.WriteFile(schemaPath, []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run([]string{"--schema", schemaPath}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'User'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("table count = %d, want 1", count)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'Post'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("post table count = %d, want 1", count)
	}

	fkRows, err := db.Query(`SELECT "table", "from", "to", on_delete FROM pragma_foreign_key_list('Post')`)
	if err != nil {
		t.Fatal(err)
	}
	defer fkRows.Close()
	if !fkRows.Next() {
		t.Fatal("expected Post foreign key")
	}
	var refTable, fromColumn, toColumn, onDelete string
	if err := fkRows.Scan(&refTable, &fromColumn, &toColumn, &onDelete); err != nil {
		t.Fatal(err)
	}
	if refTable != "User" || fromColumn != "author_id" || toColumn != "id" || onDelete != "CASCADE" {
		t.Fatalf("foreign key = %s %s %s %s", refTable, fromColumn, toColumn, onDelete)
	}
}

func findTestModel(schema *ir.Schema, name string) *ir.Model {
	for _, model := range schema.Models {
		if model.TableName() == name {
			return model
		}
	}
	return nil
}
