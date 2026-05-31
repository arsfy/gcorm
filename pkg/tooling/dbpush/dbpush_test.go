package dbpush

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

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

func TestSplitStatementsIgnoresSemicolonsInsideSQLSyntax(t *testing.T) {
	stmts, unsupported := splitStatements(`
CREATE TABLE "users" (
  "id" INTEGER NOT NULL,
  "note" TEXT NOT NULL DEFAULT 'a;b'
);
/* block comment with ; */
CREATE INDEX "idx_users_note" ON "users" ("note");
CREATE TABLE "events" ("payload" TEXT DEFAULT $$a;b$$);
`)
	if len(unsupported) != 0 {
		t.Fatalf("len(unsupported) = %d, want 0: %v", len(unsupported), unsupported)
	}
	if len(stmts) != 3 {
		t.Fatalf("len(stmts) = %d, want 3: %#v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], `DEFAULT 'a;b'`) {
		t.Fatalf("first statement lost string default: %q", stmts[0])
	}
	if !strings.Contains(stmts[2], `$$a;b$$`) {
		t.Fatalf("third statement lost dollar-quoted default: %q", stmts[2])
	}
}

func TestStatementSummaryRedactsValues(t *testing.T) {
	stmt := `ALTER TABLE "users" ADD COLUMN "api_key" TEXT DEFAULT 'internal_api_key_xxx';`
	got := statementSummary(stmt)
	if strings.Contains(got, "internal_api_key_xxx") || strings.Contains(got, "DEFAULT") {
		t.Fatalf("statementSummary leaked sensitive SQL: %q", got)
	}
	if got != `ALTER TABLE "users" ADD` {
		t.Fatalf("statementSummary() = %q", got)
	}
}

func TestValidateNoInternalTableConflict(t *testing.T) {
	schema := &ir.Schema{
		Models: []*ir.Model{{Name: "PushLog", DBName: schemaPushesTable}},
	}
	if err := validateNoInternalTableConflict(schema); err == nil {
		t.Fatal("expected reserved table conflict")
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

	jsonObject := postgresDefaultValue("'{}'::jsonb", false)
	if jsonObject == nil || !jsonObject.IsString || jsonObject.Value != "{}" {
		t.Fatalf("json object default = %#v, want {} string", jsonObject)
	}

	number := postgresDefaultValue("0", false)
	if number == nil || !number.IsNumber || number.Value != "0" {
		t.Fatalf("numeric default = %#v, want numeric 0", number)
	}

	array := postgresDefaultValue("'{}'::bigint[]", false)
	if array == nil || !array.IsArray || len(array.ArrayValue) != 0 {
		t.Fatalf("array default = %#v, want empty array", array)
	}

	textArray := postgresDefaultValue("'{openid,email,profile}'::text[]", false)
	if textArray == nil || !textArray.IsArray || strings.Join(textArray.ArrayValue, ",") != "openid,email,profile" {
		t.Fatalf("text array default = %#v, want openid,email,profile", textArray)
	}

	arrayExpr := postgresDefaultValue("ARRAY['openid'::text, 'email'::text, 'profile'::text]", false)
	if arrayExpr == nil || !arrayExpr.IsArray || strings.Join(arrayExpr.ArrayValue, ",") != "openid,email,profile" {
		t.Fatalf("ARRAY default = %#v, want openid,email,profile", arrayExpr)
	}
}

func TestDatabaseScalarTypeMappings(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "postgres uuid", got: postgresScalarType("uuid", "uuid"), want: "UUID"},
		{name: "postgres float4", got: postgresScalarType("real", "float4"), want: "Float"},
		{name: "postgres numeric", got: postgresScalarType("numeric", "numeric"), want: "Decimal"},
		{name: "postgres bool", got: postgresScalarType("boolean", "bool"), want: "Boolean"},
		{name: "postgres timestamptz", got: postgresScalarType("timestamp with time zone", "timestamptz"), want: "DateTime"},
		{name: "postgres bytea", got: postgresScalarType("bytea", "bytea"), want: "Bytes"},
		{name: "postgres jsonb", got: postgresScalarType("jsonb", "jsonb"), want: "Json"},
		{name: "mysql uuid char", got: mysqlScalarType("char", "char(36)"), want: "UUID"},
		{name: "mysql tinyint boolean", got: mysqlScalarType("tinyint", "tinyint(1)"), want: "Boolean"},
		{name: "mysql mediumint", got: mysqlScalarType("mediumint", "mediumint"), want: "Int"},
		{name: "mysql decimal", got: mysqlScalarType("decimal", "decimal(10,2)"), want: "Decimal"},
		{name: "mysql blob", got: mysqlScalarType("blob", "blob"), want: "Bytes"},
		{name: "mysql json", got: mysqlScalarType("json", "json"), want: "Json"},
		{name: "sqlite real", got: sqliteScalarType("REAL"), want: "Float"},
		{name: "sqlite numeric", got: sqliteScalarType("NUMERIC"), want: "Decimal"},
		{name: "sqlite bool", got: sqliteScalarType("BOOLEAN"), want: "Boolean"},
		{name: "sqlite datetime", got: sqliteScalarType("DATETIME"), want: "DateTime"},
		{name: "sqlite blob", got: sqliteScalarType("BLOB"), want: "Bytes"},
		{name: "sqlite json", got: sqliteScalarType("JSON"), want: "Json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("mapping = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestDatabaseDefaultValueMappings(t *testing.T) {
	mysqlAuto := mysqlDefaultValue("", "auto_increment")
	if mysqlAuto == nil || !mysqlAuto.IsFunction || mysqlAuto.FuncName != "autoincrement" {
		t.Fatalf("mysql auto increment default = %#v", mysqlAuto)
	}

	mysqlNow := mysqlDefaultValue("CURRENT_TIMESTAMP", "")
	if mysqlNow == nil || !mysqlNow.IsFunction || mysqlNow.FuncName != "now" {
		t.Fatalf("mysql timestamp default = %#v", mysqlNow)
	}

	mysqlBool := mysqlDefaultValue("1", "")
	if mysqlBool == nil || !mysqlBool.IsBool || mysqlBool.Value != "true" {
		t.Fatalf("mysql bool default = %#v", mysqlBool)
	}

	sqliteUUID := sqliteDefaultValue("(lower(hex(randomblob(16))))")
	if sqliteUUID == nil || !sqliteUUID.IsFunction || sqliteUUID.FuncName != "uuid" {
		t.Fatalf("sqlite uuid default = %#v", sqliteUUID)
	}

	sqliteNow := sqliteDefaultValue("CURRENT_TIMESTAMP")
	if sqliteNow == nil || !sqliteNow.IsFunction || sqliteNow.FuncName != "now" {
		t.Fatalf("sqlite timestamp default = %#v", sqliteNow)
	}

	sqliteString := sqliteDefaultValue("'draft'")
	if sqliteString == nil || !sqliteString.IsString || sqliteString.Value != "draft" {
		t.Fatalf("sqlite string default = %#v", sqliteString)
	}

	sqliteSmallIntZero := sqliteDefaultValueForScalar("(0)", "SmallInt")
	if sqliteSmallIntZero == nil || !sqliteSmallIntZero.IsNumber || sqliteSmallIntZero.Value != "0" {
		t.Fatalf("sqlite smallint zero default = %#v, want numeric 0", sqliteSmallIntZero)
	}

	sqliteBoolFalse := sqliteDefaultValueForScalar("(0)", "Boolean")
	if sqliteBoolFalse == nil || !sqliteBoolFalse.IsBool || sqliteBoolFalse.Value != "false" {
		t.Fatalf("sqlite bool false default = %#v", sqliteBoolFalse)
	}
}

func TestPostgresTextArrayAndIdentifierQuoting(t *testing.T) {
	if got := parsePostgresTextArray(`{id,"created_at",status}`); strings.Join(got, ",") != "id,created_at,status" {
		t.Fatalf("parsePostgresTextArray() = %#v", got)
	}
	if got := parsePostgresTextArray("{}"); got != nil {
		t.Fatalf("parsePostgresTextArray(empty) = %#v, want nil", got)
	}
	if got := sqliteQuoteIdent(`weird"name`); got != `"weird""name"` {
		t.Fatalf("sqliteQuoteIdent() = %q", got)
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

func TestPushEmbeddedSQLiteInitialNoopAndFollowUp(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "embedded.db")
	dsn := "file:" + filepath.ToSlash(dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	initialFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url      = env("DATABASE_URL")
}

model User {
  id    String @id
  email String @unique
}
`)},
	}

	ctx := context.Background()
	result, err := Push(ctx, db, Options{
		SchemaFS:    initialFS,
		SchemaRoot:  "schema",
		DatabaseURL: dsn,
	})
	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if result.Noop || result.ChangeCount == 0 || len(result.Statements) == 0 {
		t.Fatalf("initial Push() result = %#v, want applied changes", result)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'User'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("User table count = %d, want 1", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM __gco_schema_pushes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("metadata row count = %d, want 1", count)
	}

	noop, err := Push(ctx, db, Options{
		SchemaFS:    initialFS,
		SchemaRoot:  "schema",
		DatabaseURL: dsn,
	})
	if err != nil {
		t.Fatalf("second Push() error = %v", err)
	}
	if !noop.Noop || noop.ChangeCount != 0 {
		t.Fatalf("second Push() result = %#v, want noop", noop)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM __gco_schema_pushes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("metadata row count after noop = %d, want 2", count)
	}

	updatedFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url      = env("DATABASE_URL")
}

model User {
  id    String @id
  email String @unique
  name  String?
}
`)},
	}
	updated, err := Push(ctx, db, Options{
		SchemaFS:    updatedFS,
		SchemaRoot:  "schema",
		DatabaseURL: dsn,
	})
	if err != nil {
		t.Fatalf("updated Push() error = %v", err)
	}
	if updated.Noop || updated.ChangeCount == 0 {
		t.Fatalf("updated Push() result = %#v, want applied change", updated)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('User') WHERE name = 'name'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("name column count = %d, want 1", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM __gco_schema_pushes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("metadata row count after update = %d, want 3", count)
	}

	hashRows, err := db.Query(`SELECT schema_hash FROM __gco_schema_pushes ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer hashRows.Close()
	var hashes []string
	for hashRows.Next() {
		var hash string
		if err := hashRows.Scan(&hash); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, hash)
	}
	if err := hashRows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 3 {
		t.Fatalf("schema hash count = %d, want 3", len(hashes))
	}
	if hashes[0] != hashes[1] {
		t.Fatalf("noop push should record the same schema hash: %v", hashes)
	}
	if hashes[0] == hashes[2] {
		t.Fatalf("schema hashes should differ after schema change: %v", hashes)
	}
}

func TestPushEmbeddedSQLiteDryRunDoesNotApply(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dryrun.db")
	dsn := "file:" + filepath.ToSlash(dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schemaFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url      = env("DATABASE_URL")
}

model User {
  id    String @id
  email String @unique
}
`)},
	}

	result, err := Push(context.Background(), db, Options{
		SchemaFS:    schemaFS,
		SchemaRoot:  "schema",
		DatabaseURL: dsn,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if result.Noop || len(result.Statements) == 0 {
		t.Fatalf("dry-run result = %#v, want planned statements", result)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'User'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("User table count = %d, want 0", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = '__gco_schema_pushes'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("metadata table count = %d, want 0", count)
	}
}

func TestPushEmbeddedDryRunSkipIntrospectionPlansWithoutDatabase(t *testing.T) {
	schemaFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url      = env("DATABASE_URL")
}

model User {
  id    String @id
  email String @unique
}
`)},
	}

	result, err := Push(context.Background(), nil, Options{
		SchemaFS:          schemaFS,
		SchemaRoot:        "schema",
		DryRun:            true,
		SkipIntrospection: true,
	})
	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if result.Noop || result.ChangeCount == 0 || len(result.Statements) == 0 {
		t.Fatalf("dry-run local result = %#v, want planned init statements", result)
	}
}

func TestPushEmbeddedSkipIntrospectionRequiresDryRun(t *testing.T) {
	schemaFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url      = "file:test.db"
}

model User {
  id String @id
}
`)},
	}

	_, err := Push(context.Background(), nil, Options{
		SchemaFS:          schemaFS,
		SchemaRoot:        "schema",
		SkipIntrospection: true,
	})
	if err == nil || !strings.Contains(err.Error(), "SkipIntrospection requires DryRun") {
		t.Fatalf("Push() error = %v, want SkipIntrospection/DryRun validation", err)
	}
}

func TestRunSQLiteDBPushCreatesAutoIncrementPrimaryKey(t *testing.T) {
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

model Provider {
  id        Int      @id @default(autoincrement())
  provider  String
  name      String
  status    SmallInt @default(0)
  data      Json
  createdAt DateTime @default(now())
  updatedAt DateTime @updatedAt
}

model Usage {
  id            Int      @id @default(autoincrement())
  weeklyUsage   BigInt
  windowUsage   Decimal
  weeklyResetAt DateTime
  windowResetAt DateTime
  createdAt     DateTime @default(now())
  updatedAt     DateTime @updatedAt
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

	var createSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'Provider'`).Scan(&createSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(createSQL, `"id" INTEGER PRIMARY KEY AUTOINCREMENT`) {
		t.Fatalf("create SQL missing inline autoincrement primary key:\n%s", createSQL)
	}

	if err := Run([]string{"--schema", schemaPath}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
}

func TestRunSQLiteDBPushBooleanStorageCanBecomeSmallIntDefaultZero(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dev.db")

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE "Provider" (
  "id" INTEGER PRIMARY KEY AUTOINCREMENT,
  "status" INTEGER NOT NULL DEFAULT 0
)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	schemaPath := filepath.Join(dir, "schema.gcorm")
	schema := `datasource db {
  provider = "sqlite"
  url      = "file:` + filepath.ToSlash(dbPath) + `"
}

generator client {
  provider = "gco-go"
  output   = "./gen"
}

model Provider {
  id     Int      @id @default(autoincrement())
  status SmallInt @default(0)
}
`
	if err := os.WriteFile(schemaPath, []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run([]string{"--schema", schemaPath}); err != nil {
		t.Fatalf("Run() error = %v", err)
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
