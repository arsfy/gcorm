package dbpush

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"

	_ "modernc.org/sqlite"
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

func TestResolveURLAppliesDatasourceSchemaToSearchPath(t *testing.T) {
	schema := &ir.Schema{Datasource: &ir.Datasource{Provider: "postgresql", URL: "postgresql://localhost/app", Schema: "tenant"}}
	got, _, err := resolveURL("", schema)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	if value := u.Query().Get("search_path"); value != "tenant" {
		t.Fatalf("search_path=%q", value)
	}
	schema.Datasource.URL = "postgresql://localhost/app?search_path=other"
	if _, _, err := resolveURL("", schema); err == nil {
		t.Fatal("expected conflicting search_path error")
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
`, "sqlite")
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
`, "postgresql")
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

func TestSplitStatementsKeepsCommentMarkersInsideStrings(t *testing.T) {
	stmts, unsupported := splitStatements(`
CREATE TABLE "notes" (
  "id" INTEGER NOT NULL,
  "body" TEXT DEFAULT 'line1
-- not a comment
line2'
);
`, "postgresql")
	if len(unsupported) != 0 {
		t.Fatalf("len(unsupported) = %d, want 0: %v", len(unsupported), unsupported)
	}
	if len(stmts) != 1 {
		t.Fatalf("len(stmts) = %d, want 1: %#v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "-- not a comment") {
		t.Fatalf("statement lost string content: %q", stmts[0])
	}
}

func TestSplitStatementsIgnoresSemicolonsAfterBackslashEscapedQuotes(t *testing.T) {
	stmts, unsupported := splitStatements(`
CREATE TABLE "notes" ("body" TEXT DEFAULT E'it\'s;a;note');
CREATE TABLE "users" ("id" INTEGER NOT NULL);
`, "postgresql")
	if len(unsupported) != 0 {
		t.Fatalf("len(unsupported) = %d, want 0: %v", len(unsupported), unsupported)
	}
	if len(stmts) != 2 {
		t.Fatalf("len(stmts) = %d, want 2: %#v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], `E'it\'s;a;note'`) {
		t.Fatalf("first statement lost escaped string default: %q", stmts[0])
	}
}

func TestSplitStatementsBackslashNotEscapeInStandardStrings(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		dialect string
		want    int
	}{
		{
			name:    "postgres trailing backslash in string default",
			sql:     "CREATE TABLE \"files\" (\"path\" TEXT DEFAULT 'C:\\');\nCREATE TABLE \"users\" (\"id\" INTEGER NOT NULL);",
			dialect: "postgresql",
			want:    2,
		},
		{
			name:    "sqlite trailing backslash in string default",
			sql:     "CREATE TABLE \"files\" (\"path\" TEXT DEFAULT 'C:\\');\nCREATE TABLE \"users\" (\"id\" INTEGER NOT NULL);",
			dialect: "sqlite",
			want:    2,
		},
		{
			name:    "postgres escape string keeps backslash semantics",
			sql:     "CREATE TABLE \"notes\" (\"body\" TEXT DEFAULT E'it\\'s;a');\nCREATE TABLE \"users\" (\"id\" INTEGER NOT NULL);",
			dialect: "postgresql",
			want:    2,
		},
		{
			name:    "mysql string keeps backslash escapes",
			sql:     "CREATE TABLE `notes` (`body` TEXT DEFAULT 'it\\'s;a');\nCREATE TABLE `users` (`id` INT);",
			dialect: "mysql",
			want:    2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts, unsupported := splitStatements(tt.sql, tt.dialect)
			if len(unsupported) != 0 {
				t.Fatalf("len(unsupported) = %d, want 0: %v", len(unsupported), unsupported)
			}
			if len(stmts) != tt.want {
				t.Fatalf("len(stmts) = %d, want %d: %#v", len(stmts), tt.want, stmts)
			}
		})
	}

	t.Run("strip comments after backslash-terminated string", func(t *testing.T) {
		cleaned, unsupported := stripTopLevelSQLComments(`SELECT 'C:\' -- trailing note`, "postgresql")
		if len(unsupported) != 1 || unsupported[0] != "-- trailing note" {
			t.Fatalf("unsupported = %#v, want [-- trailing note]", unsupported)
		}
		if !strings.Contains(cleaned, `'C:\'`) {
			t.Fatalf("cleaned lost string literal: %q", cleaned)
		}
	})
}

func TestSplitStatementsRemovesTopLevelBlockComments(t *testing.T) {
	stmts, unsupported := splitStatements(`
/* generated comment with ; */
CREATE /* inline comment */ TABLE "users" ("id" INTEGER NOT NULL);
`, "postgresql")
	if len(unsupported) != 0 {
		t.Fatalf("len(unsupported) = %d, want 0: %v", len(unsupported), unsupported)
	}
	if len(stmts) != 1 {
		t.Fatalf("len(stmts) = %d, want 1: %#v", len(stmts), stmts)
	}
	if strings.Contains(stmts[0], "comment") {
		t.Fatalf("statement kept block comment: %q", stmts[0])
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

func TestApplyPostgresColumnTypeRestoresEnumAndNativeText(t *testing.T) {
	t.Run("enum", func(t *testing.T) {
		field := &ir.Field{Type: ir.FieldKindScalar}
		applyPostgresColumnType(field, "USER-DEFINED", "ClusterPermission", `"ClusterPermission"`, nil, map[string]string{
			"ClusterPermission": "ClusterPermission",
		})
		if field.Type != ir.FieldKindEnum || field.EnumType != "ClusterPermission" || field.ScalarType != "" {
			t.Fatalf("field = %#v, want ClusterPermission enum", field)
		}
	})

	t.Run("mapped enum array", func(t *testing.T) {
		field := &ir.Field{Type: ir.FieldKindScalar}
		applyPostgresColumnType(field, "ARRAY", "_cluster_permission", `cluster_permission[]`, nil, map[string]string{
			"cluster_permission": "ClusterPermission",
		})
		if field.Type != ir.FieldKindEnum || field.EnumType != "ClusterPermission" || !field.IsList {
			t.Fatalf("field = %#v, want ClusterPermission enum array", field)
		}
	})

	t.Run("native text", func(t *testing.T) {
		field := &ir.Field{Type: ir.FieldKindScalar}
		target := &ir.Field{
			Type:       ir.FieldKindScalar,
			ScalarType: "String",
			NativeType: &ir.NativeType{Name: "Text"},
		}
		applyPostgresColumnType(field, "text", "text", "text", target, nil)
		if field.ScalarType != "String" || field.NativeType == nil || field.NativeType.Name != "Text" {
			t.Fatalf("field = %#v, want String @db.Text", field)
		}
	})
}

func TestPostgresIntrospectedEnumUsesTargetMappings(t *testing.T) {
	target := &ir.Enum{
		Name:   "ClusterPermission",
		DBName: "cluster_permission",
		Values: []*ir.EnumValue{{Name: "OWNER", DBName: "owner"}, {Name: "OPERATOR"}},
	}
	enum := postgresIntrospectedEnum("cluster_permission", target)
	enum.Values = append(enum.Values,
		postgresIntrospectedEnumValue("owner", target),
		postgresIntrospectedEnumValue("OPERATOR", target),
	)
	if enum.Name != "ClusterPermission" || enum.DBName != "cluster_permission" {
		t.Fatalf("enum = %#v", enum)
	}
	if enum.Values[0].Name != "OWNER" || enum.Values[0].DBName != "owner" || enum.Values[1].Name != "OPERATOR" {
		t.Fatalf("values = %#v", enum.Values)
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

	mysqlNow := mysqlDefaultValue("CURRENT_TIMESTAMP", "DEFAULT_GENERATED")
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
	if got := parsePostgresTextArray(`{"lower(COALESCE(email, ''))",email}`); len(got) != 2 || got[0] != `lower(COALESCE(email, ''))` {
		t.Fatalf("parsePostgresTextArray(expression) = %#v", got)
	}
	if got := sqliteQuoteIdent(`weird"name`); got != `"weird""name"` {
		t.Fatalf("sqliteQuoteIdent() = %q", got)
	}
}

func TestParsePostgresExpressionIndexColumn(t *testing.T) {
	cols := parsePostgresIndexColumns(
		[]string{""},
		[]string{`lower(email) text_ops DESC NULLS LAST`},
		[]string{"1"},
		[]string{"text_ops"},
		nil,
	)
	if len(cols) != 1 {
		t.Fatalf("columns = %d, want 1", len(cols))
	}
	col := cols[0]
	if col.Field != "" || col.Expression != "lower(email)" || col.Sort != "DESC" || col.Nulls != "LAST" || col.OpClass != "text_ops" {
		t.Fatalf("expression column = %+v", col)
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
	if len(result.AppliedStatements) != len(result.Statements) {
		t.Fatalf("initial applied statements = %d, want %d", len(result.AppliedStatements), len(result.Statements))
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
	if len(noop.AppliedStatements) != 0 {
		t.Fatalf("noop applied statements = %d, want 0", len(noop.AppliedStatements))
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
	if len(updated.AppliedStatements) != len(updated.Statements) {
		t.Fatalf("updated applied statements = %d, want %d", len(updated.AppliedStatements), len(updated.Statements))
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

// stubRunner implements sqlRunner for error-path and statement-capture tests.
type stubRunner struct {
	execErr  error
	lastStmt string
	lastArgs []any
}

func (s *stubRunner) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	s.lastStmt = query
	s.lastArgs = args
	return stubResult{}, s.execErr
}

func (s *stubRunner) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("stubRunner: QueryContext not implemented")
}

func (s *stubRunner) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

type stubResult struct{}

func (stubResult) LastInsertId() (int64, error) { return 0, nil }
func (stubResult) RowsAffected() (int64, error) { return 0, nil }

func openTestSQLite(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "test.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dsn
}

func TestPushValidationErrors(t *testing.T) {
	t.Run("missing SchemaFS", func(t *testing.T) {
		_, err := Push(context.Background(), nil, Options{})
		if err == nil || !strings.Contains(err.Error(), "SchemaFS is required") {
			t.Fatalf("Push() error = %v, want SchemaFS validation", err)
		}
	})

	t.Run("nil database", func(t *testing.T) {
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
		_, err := Push(context.Background(), nil, Options{SchemaFS: schemaFS, SchemaRoot: "schema"})
		if err == nil || !strings.Contains(err.Error(), "nil database") {
			t.Fatalf("Push() error = %v, want nil database validation", err)
		}
	})

	t.Run("missing schema files", func(t *testing.T) {
		_, err := Push(context.Background(), nil, Options{SchemaFS: fstest.MapFS{}, SchemaRoot: "schema"})
		if err == nil {
			t.Fatal("Push() error = nil, want missing schema files error")
		}
	})
}

func TestPushRejectsMultiplePostgresSchemas(t *testing.T) {
	db, _ := openTestSQLite(t)

	schemaFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "postgresql"
  url      = "postgresql://localhost/app"
  schemas  = ["app", "billing"]
}

model User {
  id String @id
}
`)},
	}

	_, err := Push(context.Background(), db, Options{SchemaFS: schemaFS, SchemaRoot: "schema"})
	if err == nil || !strings.Contains(err.Error(), "multiple postgresql datasource schemas") {
		t.Fatalf("Push() error = %v, want multiple schemas rejection", err)
	}
}

func TestPushReportsSearchPathErrorOnWrongDriver(t *testing.T) {
	db, _ := openTestSQLite(t)

	schemaFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "postgresql"
  url      = "postgresql://localhost/app"
  schema   = "app"
}

model User {
  id String @id
}
`)},
	}

	// A postgresql schema pushed through a sqlite connection fails when the
	// transaction tries to set the postgresql search_path.
	_, err := Push(context.Background(), db, Options{SchemaFS: schemaFS, SchemaRoot: "schema"})
	if err == nil || !strings.Contains(err.Error(), "set postgresql search_path") {
		t.Fatalf("Push() error = %v, want search_path failure", err)
	}
}

func TestPushEmbeddedRefusesDestructiveChangeWithoutForce(t *testing.T) {
	db, dsn := openTestSQLite(t)

	v1 := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url      = "file:test.db"
}

model User {
  id    String @id
  email String @unique
  name  String?
}
`)},
	}
	v2 := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url      = "file:test.db"
}

model User {
  id    String @id
  email String @unique
}
`)},
	}

	ctx := context.Background()
	if _, err := Push(ctx, db, Options{SchemaFS: v1, SchemaRoot: "schema", DatabaseURL: dsn}); err != nil {
		t.Fatalf("initial Push() error = %v", err)
	}

	_, err := Push(ctx, db, Options{SchemaFS: v2, SchemaRoot: "schema", DatabaseURL: dsn})
	if err == nil || !strings.Contains(err.Error(), "refused to apply") {
		t.Fatalf("destructive Push() error = %v, want refusal without force", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('User') WHERE name = 'name'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("name column count after refused push = %d, want 1", count)
	}

	result, err := Push(ctx, db, Options{SchemaFS: v2, SchemaRoot: "schema", DatabaseURL: dsn, AllowDestructive: true})
	if err != nil {
		t.Fatalf("forced Push() error = %v", err)
	}
	if result.Noop || len(result.AppliedStatements) == 0 {
		t.Fatalf("forced Push() result = %#v, want applied destructive change", result)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('User') WHERE name = 'name'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("name column count after forced push = %d, want 0", count)
	}
}

func TestPushEmbeddedDryRunReportsNoop(t *testing.T) {
	db, dsn := openTestSQLite(t)

	schemaFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url      = "file:test.db"
}

model User {
  id    String @id
  email String @unique
}
`)},
	}

	ctx := context.Background()
	if _, err := Push(ctx, db, Options{SchemaFS: schemaFS, SchemaRoot: "schema", DatabaseURL: dsn}); err != nil {
		t.Fatalf("initial Push() error = %v", err)
	}

	result, err := Push(ctx, db, Options{SchemaFS: schemaFS, SchemaRoot: "schema", DatabaseURL: dsn, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run Push() error = %v", err)
	}
	if !result.Noop || result.ChangeCount != 0 || len(result.Statements) != 0 {
		t.Fatalf("dry-run noop result = %#v, want noop with no statements", result)
	}
}

func TestRecordNoopPush(t *testing.T) {
	t.Run("records metadata", func(t *testing.T) {
		db, _ := openTestSQLite(t)

		result, err := recordNoopPush(context.Background(), db, "sqlite", "abc123", 2, Options{})
		if err != nil {
			t.Fatalf("recordNoopPush() error = %v", err)
		}
		if !result.Noop || result.ChangeCount != 0 || result.Provider != "sqlite" || result.SchemaHash != "abc123" || result.ModelCount != 2 {
			t.Fatalf("recordNoopPush() result = %#v", result)
		}

		var hash string
		var modelCount int
		if err := db.QueryRow(`SELECT schema_hash, model_count FROM __gco_schema_pushes`).Scan(&hash, &modelCount); err != nil {
			t.Fatal(err)
		}
		if hash != "abc123" || modelCount != 2 {
			t.Fatalf("metadata row = %q, %d", hash, modelCount)
		}
	})

	t.Run("disable lock", func(t *testing.T) {
		db, _ := openTestSQLite(t)
		if _, err := recordNoopPush(context.Background(), db, "sqlite", "def456", 1, Options{DisableLock: true}); err != nil {
			t.Fatalf("recordNoopPush() error = %v", err)
		}
	})

	t.Run("unsupported lock provider", func(t *testing.T) {
		db, _ := openTestSQLite(t)
		_, err := recordNoopPush(context.Background(), db, "mysql", "abc123", 1, Options{})
		if err == nil || !strings.Contains(err.Error(), "db push lock unsupported") {
			t.Fatalf("recordNoopPush() error = %v, want unsupported lock error", err)
		}
	})
}

func TestPlanWithoutIntrospectionEdgeCases(t *testing.T) {
	t.Run("empty schema is noop", func(t *testing.T) {
		target := &ir.Schema{Datasource: &ir.Datasource{Provider: "sqlite"}}
		result, err := planWithoutIntrospection(target, "hash")
		if err != nil {
			t.Fatalf("planWithoutIntrospection() error = %v", err)
		}
		if !result.Noop || result.ChangeCount != 0 || len(result.Statements) != 0 {
			t.Fatalf("planWithoutIntrospection() = %#v, want noop", result)
		}
	})

	t.Run("unsupported provider", func(t *testing.T) {
		target := &ir.Schema{Datasource: &ir.Datasource{Provider: "sqlserver"}}
		_, err := planWithoutIntrospection(target, "hash")
		if err == nil || !strings.Contains(err.Error(), "db push supports postgresql, mysql, and sqlite") {
			t.Fatalf("planWithoutIntrospection() error = %v", err)
		}
	})

	t.Run("internal table conflict", func(t *testing.T) {
		target := &ir.Schema{
			Datasource: &ir.Datasource{Provider: "sqlite"},
			Models:     []*ir.Model{{Name: "Migration", DBName: migrationsTable}},
		}
		_, err := planWithoutIntrospection(target, "hash")
		if err == nil || !strings.Contains(err.Error(), "reserved GCORM internal table") {
			t.Fatalf("planWithoutIntrospection() error = %v", err)
		}
	})
}

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		name    string
		schema  *ir.Schema
		want    string
		wantErr string
	}{
		{name: "nil schema defaults to postgresql", schema: nil, want: "postgresql"},
		{name: "nil datasource defaults to postgresql", schema: &ir.Schema{}, want: "postgresql"},
		{name: "explicit sqlite", schema: &ir.Schema{Datasource: &ir.Datasource{Provider: "sqlite"}}, want: "sqlite"},
		{name: "unsupported", schema: &ir.Schema{Datasource: &ir.Datasource{Provider: "sqlserver"}}, wantErr: "db push supports postgresql, mysql, and sqlite"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectProvider(tt.schema)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("detectProvider() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("detectProvider() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("detectProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProviderFallbacks(t *testing.T) {
	if isSupportedProvider("oracle") {
		t.Fatal("oracle should not be a supported provider")
	}
	if got := driverName("oracle"); got != "oracle" {
		t.Fatalf("driverName(oracle) = %q, want passthrough", got)
	}
}

func TestNormalizeConnectionURLPassthrough(t *testing.T) {
	got, err := NormalizeConnectionURL("custom://example", "oracle")
	if err != nil {
		t.Fatalf("NormalizeConnectionURL() error = %v", err)
	}
	if got != "custom://example" {
		t.Fatalf("NormalizeConnectionURL() = %q, want passthrough", got)
	}
}

func TestNormalizePostgresURLEdgeCases(t *testing.T) {
	t.Run("parse error", func(t *testing.T) {
		if _, err := normalizePostgresURL("postgresql://h%zz/db"); err == nil {
			t.Fatal("expected parse error")
		}
	})

	t.Run("scheme-less passthrough", func(t *testing.T) {
		raw := "host=localhost port=5432"
		got, err := normalizePostgresURL(raw)
		if err != nil {
			t.Fatalf("normalizePostgresURL() error = %v", err)
		}
		if got != raw {
			t.Fatalf("normalizePostgresURL() = %q, want passthrough %q", got, raw)
		}
	})

	t.Run("remote host keeps sslmode unset", func(t *testing.T) {
		got, err := normalizePostgresURL("postgresql://db.example.com/app")
		if err != nil {
			t.Fatalf("normalizePostgresURL() error = %v", err)
		}
		u, _ := url.Parse(got)
		if sslmode := u.Query().Get("sslmode"); sslmode != "" {
			t.Fatalf("sslmode = %q, want empty for remote host", sslmode)
		}
	})
}

func TestNormalizeMySQLURLEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "unix socket passthrough", raw: "user:secret@unix(/tmp/mysql.sock)/app", want: "user:secret@unix(/tmp/mysql.sock)/app"},
		{name: "url without credentials or port", raw: "mysql://dbhost/app", want: "tcp(dbhost)/app"},
		{name: "url without host defaults to localhost", raw: "mysql:///app", want: "tcp(localhost:3306)/app"},
		{name: "non-mysql scheme passthrough", raw: "postgres://localhost/app", want: "postgres://localhost/app"},
		{name: "parse error", raw: "mysql://%zz", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeMySQLURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeMySQLURL(%q) error = nil, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeMySQLURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeMySQLURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeSQLiteURLEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty sqlite URL is in-memory", raw: "sqlite://", want: ":memory:"},
		{name: "sqlite URL with host", raw: "sqlite://data/app.db", want: "/data/app.db"},
		{name: "plain path passthrough", raw: "dev.db", want: "dev.db"},
		{name: "parse error", raw: "sqlite://%zz", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeSQLiteURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeSQLiteURL(%q) error = nil, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeSQLiteURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeSQLiteURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestIsLocalPostgresHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"", false},
		{"localhost", true},
		{"LOCALHOST", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"db.example.com", false},
		{"10.0.0.5", false},
		{"not-an-ip", false},
	}
	for _, tt := range tests {
		if got := isLocalPostgresHost(tt.host); got != tt.want {
			t.Fatalf("isLocalPostgresHost(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestResolveURLEnvEdgeCases(t *testing.T) {
	t.Run("env url without variable name", func(t *testing.T) {
		schema := &ir.Schema{Datasource: &ir.Datasource{Provider: "postgresql", URLIsEnv: true}}
		_, _, err := resolveURL("", schema)
		if err == nil || !strings.Contains(err.Error(), "no variable name was provided") {
			t.Fatalf("resolveURL() error = %v", err)
		}
	})

	t.Run("explicit url with nil schema", func(t *testing.T) {
		got, source, err := resolveURL("postgresql://localhost/app", nil)
		if err != nil {
			t.Fatalf("resolveURL() error = %v", err)
		}
		if source != "--url" {
			t.Fatalf("resolveURL() source = %q, want %q", source, "--url")
		}
		u, _ := url.Parse(got)
		if u.Query().Get("sslmode") != "disable" {
			t.Fatalf("resolveURL() sslmode = %q, want disable for localhost", u.Query().Get("sslmode"))
		}
	})
}

func TestResolveSchemaNameEdgeCases(t *testing.T) {
	t.Run("single schemas entry", func(t *testing.T) {
		schema := &ir.Schema{Datasource: &ir.Datasource{Schemas: []string{"tenant"}}}
		if got := resolveSchemaName(schema, ""); got != "tenant" {
			t.Fatalf("resolveSchemaName() = %q, want %q", got, "tenant")
		}
	})

	t.Run("default public", func(t *testing.T) {
		if got := resolveSchemaName(nil, ""); got != "public" {
			t.Fatalf("resolveSchemaName() = %q, want %q", got, "public")
		}
	})
}

func TestPostgresSchemaFromURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "schema query param", raw: "postgresql://h/db?schema=tenant", want: "tenant"},
		{name: "quoted search_path", raw: `postgresql://h/db?search_path="tenant",public`, want: "tenant"},
		{name: "no schema config", raw: "postgresql://h/db", want: ""},
		{name: "invalid URL", raw: "postgresql://h%zz/db", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresSchemaFromURL(tt.raw); got != tt.want {
				t.Fatalf("postgresSchemaFromURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestWithPostgresSearchPathEdgeCases(t *testing.T) {
	t.Run("nil datasource", func(t *testing.T) {
		got, err := withPostgresSearchPath("postgresql://h/db", nil)
		if err != nil || got != "postgresql://h/db" {
			t.Fatalf("withPostgresSearchPath() = %q, %v", got, err)
		}
	})

	t.Run("non-postgres provider", func(t *testing.T) {
		got, err := withPostgresSearchPath("file:dev.db", &ir.Datasource{Provider: "sqlite", Schema: "app"})
		if err != nil || got != "file:dev.db" {
			t.Fatalf("withPostgresSearchPath() = %q, %v", got, err)
		}
	})

	t.Run("matching existing search_path is accepted", func(t *testing.T) {
		ds := &ir.Datasource{Provider: "postgresql", Schema: "app"}
		got, err := withPostgresSearchPath("postgresql://h/db?search_path=app", ds)
		if err != nil {
			t.Fatalf("withPostgresSearchPath() error = %v", err)
		}
		u, _ := url.Parse(got)
		if u.Query().Get("search_path") != "app" {
			t.Fatalf("search_path = %q", u.Query().Get("search_path"))
		}
	})

	t.Run("scheme-less URL passthrough", func(t *testing.T) {
		ds := &ir.Datasource{Provider: "postgresql", Schema: "app"}
		got, err := withPostgresSearchPath("host=localhost", ds)
		if err != nil || got != "host=localhost" {
			t.Fatalf("withPostgresSearchPath() = %q, %v", got, err)
		}
	})
}

func TestStatementSummaryEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		stmt string
		want string
	}{
		{name: "empty", stmt: "", want: "<empty statement>"},
		{name: "whitespace only", stmt: "   \n\t ", want: "<empty statement>"},
		{name: "short statement", stmt: "DROP TABLE users;", want: "DROP TABLE users;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statementSummary(tt.stmt); got != tt.want {
				t.Fatalf("statementSummary(%q) = %q, want %q", tt.stmt, got, tt.want)
			}
		})
	}

	t.Run("long statement is truncated", func(t *testing.T) {
		got := statementSummary(strings.Repeat("x", 200))
		if len(got) != 123 || !strings.HasSuffix(got, "...") {
			t.Fatalf("statementSummary() = %q (len %d), want 120 chars plus ellipsis", got, len(got))
		}
	})
}

func TestReadDollarQuoteTag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"$$", "$$"},
		{"$tag$", "$tag$"},
		{"$t_2$ body $t_2$", "$t_2$"},
		{"$", ""},
		{"$unterminated", ""},
		{"$not a tag$", ""},
		{"abc", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := readDollarQuoteTag(tt.input); got != tt.want {
			t.Fatalf("readDollarQuoteTag(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSplitStatementsQuotedIdentifiers(t *testing.T) {
	stmts, unsupported := splitStatements("CREATE TABLE `users` (`id` INT, `note` TEXT DEFAULT 'a;b');\nCREATE TABLE [logs] ([id] INT);", "mysql")
	if len(unsupported) != 0 {
		t.Fatalf("len(unsupported) = %d, want 0: %v", len(unsupported), unsupported)
	}
	if len(stmts) != 2 {
		t.Fatalf("len(stmts) = %d, want 2: %#v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "`note` TEXT DEFAULT 'a;b'") {
		t.Fatalf("first statement lost backtick content: %q", stmts[0])
	}
	if !strings.Contains(stmts[1], "[logs]") {
		t.Fatalf("second statement lost bracket identifier: %q", stmts[1])
	}
}

func TestStripTopLevelSQLCommentsEdgeCases(t *testing.T) {
	t.Run("line comment at end of input", func(t *testing.T) {
		cleaned, unsupported := stripTopLevelSQLComments("SELECT 1 -- trailing note", "sqlite")
		if len(unsupported) != 1 || unsupported[0] != "-- trailing note" {
			t.Fatalf("unsupported = %#v", unsupported)
		}
		if strings.Contains(cleaned, "trailing") {
			t.Fatalf("cleaned kept comment: %q", cleaned)
		}
	})

	t.Run("comment markers inside dollar quotes are kept", func(t *testing.T) {
		cleaned, unsupported := stripTopLevelSQLComments("SELECT $$-- not a comment$$", "postgresql")
		if len(unsupported) != 0 {
			t.Fatalf("unsupported = %#v, want none", unsupported)
		}
		if !strings.Contains(cleaned, "-- not a comment") {
			t.Fatalf("cleaned lost dollar-quoted content: %q", cleaned)
		}
	})
}

func TestLockPushTransactionUnsupportedProvider(t *testing.T) {
	db, _ := openTestSQLite(t)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if err := lockPushTransaction(context.Background(), tx, "sqlite"); err != nil {
		t.Fatalf("lockPushTransaction(sqlite) error = %v, want nil", err)
	}
	if err := lockPushTransaction(context.Background(), tx, "mysql"); err == nil || !strings.Contains(err.Error(), "db push lock unsupported") {
		t.Fatalf("lockPushTransaction(mysql) error = %v, want unsupported provider error", err)
	}
}

func TestCreateSchemaPushesTableSQL(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"mysql", "AUTO_INCREMENT"},
		{"sqlite", "AUTOINCREMENT"},
		{"postgresql", "BIGSERIAL"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			stmt := createSchemaPushesTableSQL(tt.provider)
			if !strings.Contains(stmt, schemaPushesTable) || !strings.Contains(stmt, tt.want) {
				t.Fatalf("createSchemaPushesTableSQL(%q) missing %q:\n%s", tt.provider, tt.want, stmt)
			}
		})
	}
}

func TestInsertSchemaPushRecord(t *testing.T) {
	t.Run("postgresql uses dollar placeholders", func(t *testing.T) {
		runner := &stubRunner{}
		if err := insertSchemaPushRecord(context.Background(), runner, "postgresql", "hash", 1, 2); err != nil {
			t.Fatalf("insertSchemaPushRecord() error = %v", err)
		}
		if !strings.Contains(runner.lastStmt, "$1") {
			t.Fatalf("stmt = %q, want dollar placeholders", runner.lastStmt)
		}
		if len(runner.lastArgs) != 5 || runner.lastArgs[0] != "hash" {
			t.Fatalf("args = %#v", runner.lastArgs)
		}
	})

	t.Run("exec error is wrapped", func(t *testing.T) {
		runner := &stubRunner{execErr: errors.New("boom")}
		err := insertSchemaPushRecord(context.Background(), runner, "sqlite", "hash", 1, 2)
		if err == nil || !strings.Contains(err.Error(), "record db push metadata") {
			t.Fatalf("insertSchemaPushRecord() error = %v", err)
		}
	})
}

func TestEnsureSchemaPushesTableError(t *testing.T) {
	runner := &stubRunner{execErr: errors.New("boom")}
	err := ensureSchemaPushesTable(context.Background(), runner, "sqlite")
	if err == nil || !strings.Contains(err.Error(), "create "+schemaPushesTable+" table") {
		t.Fatalf("ensureSchemaPushesTable() error = %v", err)
	}
}

func TestValidateNoInternalTableConflictEdgeCases(t *testing.T) {
	if err := validateNoInternalTableConflict(nil); err != nil {
		t.Fatalf("validateNoInternalTableConflict(nil) error = %v", err)
	}
	schema := &ir.Schema{Models: []*ir.Model{{Name: "Migration", DBName: migrationsTable}}}
	err := validateNoInternalTableConflict(schema)
	if err == nil || !strings.Contains(err.Error(), migrationsTable) {
		t.Fatalf("validateNoInternalTableConflict() error = %v", err)
	}
}

func TestIntrospectDatabaseExported(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		db, _ := openTestSQLite(t)
		if _, err := db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY, label TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}

		schema, err := IntrospectDatabase(context.Background(), db, "sqlite", nil, "")
		if err != nil {
			t.Fatalf("IntrospectDatabase() error = %v", err)
		}
		model := findTestModel(schema, "widgets")
		if model == nil {
			t.Fatal("widgets model not found")
		}
		label := findFieldByColumn(model, "label")
		if label == nil || label.IsOptional || label.ScalarType != "String" {
			t.Fatalf("label field = %#v", label)
		}
	})

	t.Run("unsupported provider", func(t *testing.T) {
		_, err := IntrospectDatabase(context.Background(), nil, "oracle", nil, "")
		if err == nil || !strings.Contains(err.Error(), `unsupported provider "oracle"`) {
			t.Fatalf("IntrospectDatabase() error = %v", err)
		}
	})
}

func TestIntrospectSQLiteSkipsInternalTables(t *testing.T) {
	db, _ := openTestSQLite(t)
	statements := []string{
		`CREATE TABLE __gco_schema_pushes (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE __gco_migrations (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE real_table (id INTEGER PRIMARY KEY)`,
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
	if len(schema.Models) != 1 || schema.Models[0].Name != "real_table" {
		t.Fatalf("models = %#v, want only real_table", schema.Models)
	}
}

func TestFindPostgresTargetField(t *testing.T) {
	schema := &ir.Schema{
		Models: []*ir.Model{{
			Name:   "User",
			DBName: "users",
			Fields: []*ir.Field{{Name: "email", DBName: "email_address"}},
		}},
	}

	if got := findPostgresTargetField(nil, "users", "email_address"); got != nil {
		t.Fatalf("findPostgresTargetField(nil schema) = %#v, want nil", got)
	}
	if got := findPostgresTargetField(schema, "unknown", "email_address"); got != nil {
		t.Fatalf("findPostgresTargetField(unknown table) = %#v, want nil", got)
	}
	got := findPostgresTargetField(schema, "users", "email_address")
	if got == nil || got.Name != "email" {
		t.Fatalf("findPostgresTargetField() = %#v, want email field", got)
	}
}

func TestPostgresEnumHelpers(t *testing.T) {
	if got := postgresEnumDatabaseName(nil); got != "" {
		t.Fatalf("postgresEnumDatabaseName(nil) = %q", got)
	}
	if got := postgresEnumDatabaseName(&ir.Enum{Name: "Status"}); got != "Status" {
		t.Fatalf("postgresEnumDatabaseName() = %q", got)
	}
	if got := postgresEnumDatabaseName(&ir.Enum{Name: "Status", DBName: "status_enum"}); got != "status_enum" {
		t.Fatalf("postgresEnumDatabaseName() = %q", got)
	}

	enum := postgresIntrospectedEnum("status_enum", nil)
	if enum.Name != "status_enum" || enum.DBName != "" {
		t.Fatalf("postgresIntrospectedEnum() = %#v", enum)
	}

	value := postgresIntrospectedEnumValue("active", nil)
	if value.Name != "active" || value.DBName != "" {
		t.Fatalf("postgresIntrospectedEnumValue() = %#v", value)
	}
	target := &ir.Enum{Name: "Status", Values: []*ir.EnumValue{{Name: "ACTIVE", DBName: "active"}}}
	value = postgresIntrospectedEnumValue("unknown_label", target)
	if value.Name != "unknown_label" {
		t.Fatalf("postgresIntrospectedEnumValue(unmapped) = %#v", value)
	}
}

func TestPostgresNativeType(t *testing.T) {
	tests := []struct {
		name      string
		formatted string
		scalar    string
		want      *ir.NativeType
	}{
		{name: "varchar", formatted: "character varying(50)", scalar: "String", want: &ir.NativeType{Name: "VarChar", Args: []string{"50"}}},
		{name: "numeric", formatted: "numeric(10,2)", scalar: "Decimal", want: &ir.NativeType{Name: "Decimal", Args: []string{"10", "2"}}},
		{name: "plain text maps to nil", formatted: "text", scalar: "String", want: nil},
		{name: "untracked type", formatted: "integer", scalar: "Int", want: nil},
		{name: "timestamp with precision", formatted: "timestamp(6) without time zone", scalar: "DateTime", want: &ir.NativeType{Name: "Timestamp", Args: []string{"6"}}},
		{name: "timestamptz with precision", formatted: "timestamp(3) with time zone", scalar: "DateTime", want: &ir.NativeType{Name: "Timestamptz", Args: []string{"3"}}},
		{name: "plain timestamp maps to nil", formatted: "timestamp without time zone", scalar: "DateTime", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := postgresNativeType(tt.formatted, tt.scalar)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("postgresNativeType() = %#v, want nil", got)
				}
				return
			}
			if got == nil || got.Name != tt.want.Name || strings.Join(got.Args, ",") != strings.Join(tt.want.Args, ",") {
				t.Fatalf("postgresNativeType() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPostgresNativeTypeMatches(t *testing.T) {
	tests := []struct {
		name      string
		formatted string
		native    *ir.NativeType
		isList    bool
		want      bool
	}{
		{name: "nil native", formatted: "text", native: nil, want: false},
		{name: "varchar alias", formatted: "character varying(50)", native: &ir.NativeType{Name: "VarChar", Args: []string{"50"}}, want: true},
		{name: "decimal alias with spaced args", formatted: "numeric(10, 2)", native: &ir.NativeType{Name: "Decimal", Args: []string{"10", "2"}}, want: true},
		{name: "timestamptz alias", formatted: "timestamp with time zone", native: &ir.NativeType{Name: "Timestamptz"}, want: true},
		{name: "timestamp alias", formatted: "timestamp without time zone", native: &ir.NativeType{Name: "Timestamp"}, want: true},
		{name: "timestamp with precision", formatted: "timestamp(6) without time zone", native: &ir.NativeType{Name: "Timestamp", Args: []string{"6"}}, want: true},
		{name: "timestamptz with precision", formatted: "timestamp(3) with time zone", native: &ir.NativeType{Name: "Timestamptz", Args: []string{"3"}}, want: true},
		{name: "timestamp precision mismatch", formatted: "timestamp(6) without time zone", native: &ir.NativeType{Name: "Timestamp", Args: []string{"3"}}, want: false},
		{name: "timestamp precision against plain", formatted: "timestamp without time zone", native: &ir.NativeType{Name: "Timestamp", Args: []string{"6"}}, want: false},
		{name: "uuid list", formatted: "uuid[]", native: &ir.NativeType{Name: "UUID"}, isList: true, want: true},
		{name: "mismatched args", formatted: "character varying(50)", native: &ir.NativeType{Name: "VarChar", Args: []string{"100"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresNativeTypeMatches(tt.formatted, tt.native, tt.isList); got != tt.want {
				t.Fatalf("postgresNativeTypeMatches(%q) = %v, want %v", tt.formatted, got, tt.want)
			}
		})
	}
}

func TestPostgresDefaultValueFunctions(t *testing.T) {
	tests := []struct {
		name     string
		def      string
		identity bool
		check    func(*ir.DefaultValue) bool
	}{
		{name: "empty without identity", def: "", check: func(d *ir.DefaultValue) bool { return d == nil }},
		{name: "nextval sequence", def: "nextval('users_id_seq'::regclass)", check: func(d *ir.DefaultValue) bool { return d != nil && d.IsFunction && d.FuncName == "autoincrement" }},
		{name: "uuid generator", def: "gen_random_uuid()", check: func(d *ir.DefaultValue) bool { return d != nil && d.IsFunction && d.FuncName == "uuid" }},
		{name: "uuid v4 generator", def: "uuid_generate_v4()", check: func(d *ir.DefaultValue) bool { return d != nil && d.IsFunction && d.FuncName == "uuid" }},
		{name: "current timestamp", def: "CURRENT_TIMESTAMP", check: func(d *ir.DefaultValue) bool { return d != nil && d.IsFunction && d.FuncName == "now" }},
		{name: "boolean literal", def: "true", check: func(d *ir.DefaultValue) bool { return d != nil && d.IsBool && d.Value == "true" }},
		{name: "quoted uuid literal", def: "'uuid-v1'::text", check: func(d *ir.DefaultValue) bool { return d != nil && !d.IsFunction && d.IsString && d.Value == "uuid-v1" }},
		{name: "quoted now literal", def: "'now()'::text", check: func(d *ir.DefaultValue) bool { return d != nil && !d.IsFunction && d.IsString && d.Value == "now()" }},
		{name: "quoted current_timestamp literal", def: "'current_timestamp'::text", check: func(d *ir.DefaultValue) bool {
			return d != nil && !d.IsFunction && d.IsString && d.Value == "current_timestamp"
		}},
		{name: "quoted uuid generator literal", def: "'gen_random_uuid()'::text", check: func(d *ir.DefaultValue) bool {
			return d != nil && !d.IsFunction && d.IsString && d.Value == "gen_random_uuid()"
		}},
		{name: "literal containing cast", def: "'a::b'::text", check: func(d *ir.DefaultValue) bool { return d != nil && !d.IsFunction && d.IsString && d.Value == "a::b" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresDefaultValue(tt.def, tt.identity); !tt.check(got) {
				t.Fatalf("postgresDefaultValue(%q) = %#v", tt.def, got)
			}
		})
	}
}

func TestPostgresArrayDefaultEdgeCases(t *testing.T) {
	if postgresDefaultCastIsArray("0") {
		t.Fatal("postgresDefaultCastIsArray(0) = true, want false")
	}
	if got := parsePostgresArrayDefault("not-an-array"); got != nil {
		t.Fatalf("parsePostgresArrayDefault() = %#v, want nil", got)
	}

	parts := splitPostgresArrayDefaultElements("'a,b', 'c'")
	if len(parts) != 2 {
		t.Fatalf("splitPostgresArrayDefaultElements() = %#v, want 2 parts", parts)
	}

	values := parsePostgresArrayDefault("ARRAY[1, 2]")
	if strings.Join(values, ",") != "1,2" {
		t.Fatalf("parsePostgresArrayDefault() = %#v", values)
	}
}

func TestMySQLNativeType(t *testing.T) {
	tests := []struct {
		name       string
		columnType string
		scalar     string
		want       *ir.NativeType
	}{
		{name: "default varchar maps to nil", columnType: "varchar(255)", scalar: "String", want: nil},
		{name: "default decimal maps to nil", columnType: "decimal(65,30)", scalar: "Decimal", want: nil},
		{name: "custom varchar", columnType: "varchar(100)", scalar: "String", want: &ir.NativeType{Name: "VarChar", Args: []string{"100"}}},
		{name: "custom char", columnType: "char(1)", scalar: "String", want: &ir.NativeType{Name: "Char", Args: []string{"1"}}},
		{name: "uuid char maps to nil", columnType: "char(36)", scalar: "UUID", want: nil},
		{name: "custom decimal", columnType: "decimal(10,2)", scalar: "Decimal", want: &ir.NativeType{Name: "Decimal", Args: []string{"10", "2"}}},
		{name: "untracked type", columnType: "int", scalar: "Int", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mysqlNativeType(tt.columnType, tt.scalar)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("mysqlNativeType() = %#v, want nil", got)
				}
				return
			}
			if got == nil || got.Name != tt.want.Name || strings.Join(got.Args, ",") != strings.Join(tt.want.Args, ",") {
				t.Fatalf("mysqlNativeType() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMySQLScalarTypeAdditionalMappings(t *testing.T) {
	tests := []struct {
		name       string
		dataType   string
		columnType string
		want       string
	}{
		{name: "bigint", dataType: "bigint", columnType: "bigint", want: "BigInt"},
		{name: "datetime", dataType: "datetime", columnType: "datetime", want: "DateTime"},
		{name: "bool", dataType: "bool", columnType: "bool", want: "Boolean"},
		{name: "varchar default string", dataType: "varchar", columnType: "varchar(255)", want: "String"},
		{name: "char(36) is uuid", dataType: "char", columnType: "char(36)", want: "UUID"},
		{name: "varchar(36) is not uuid", dataType: "varchar", columnType: "varchar(36)", want: "String"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mysqlScalarType(tt.dataType, tt.columnType); got != tt.want {
				t.Fatalf("mysqlScalarType(%q, %q) = %q, want %q", tt.dataType, tt.columnType, got, tt.want)
			}
		})
	}
}

func TestMySQLDefaultValueUUID(t *testing.T) {
	got := mysqlDefaultValue("(uuid())", "DEFAULT_GENERATED")
	if got == nil || !got.IsFunction || got.FuncName != "uuid" {
		t.Fatalf("mysqlDefaultValue() = %#v, want uuid function", got)
	}
	if got := mysqlDefaultValue("", ""); got != nil {
		t.Fatalf("mysqlDefaultValue(empty) = %#v, want nil", got)
	}
}

func TestMySQLDefaultValueUsesExpressionMetadata(t *testing.T) {
	for _, value := range []string{"uuid()", "now()", "current_timestamp", "current_timestamp(3)", "(uuid())"} {
		t.Run(value, func(t *testing.T) {
			literal := mysqlDefaultValue(value, "")
			if literal == nil || literal.IsFunction || !literal.IsString || literal.Value != value {
				t.Fatalf("literal COLUMN_DEFAULT %q = %#v", value, literal)
			}
			expression := mysqlDefaultValue(value, "DEFAULT_GENERATED")
			if expression == nil || !expression.IsFunction {
				t.Fatalf("expression COLUMN_DEFAULT %q = %#v", value, expression)
			}
		})
	}
}

func TestMySQLDefaultValueFunctionLookalikeLiterals(t *testing.T) {
	tests := []struct {
		name  string
		def   string
		extra string
		check func(*ir.DefaultValue) bool
	}{
		{name: "uuid lookalike literal", def: "uuid-v1", check: func(d *ir.DefaultValue) bool {
			return d != nil && !d.IsFunction && d.IsString && d.Value == "uuid-v1"
		}},
		{name: "quoted uuid call literal", def: "'uuid()'", check: func(d *ir.DefaultValue) bool {
			return d != nil && !d.IsFunction && d.IsString && d.Value == "uuid()"
		}},
		{name: "quoted now literal", def: "'now()'", check: func(d *ir.DefaultValue) bool {
			return d != nil && !d.IsFunction && d.IsString && d.Value == "now()"
		}},
		{name: "quoted current_timestamp literal", def: "'current_timestamp'", check: func(d *ir.DefaultValue) bool {
			return d != nil && !d.IsFunction && d.IsString && d.Value == "current_timestamp"
		}},
		{name: "uuid function", def: "uuid()", extra: "DEFAULT_GENERATED", check: func(d *ir.DefaultValue) bool {
			return d != nil && d.IsFunction && d.FuncName == "uuid"
		}},
		{name: "current_timestamp with precision", def: "current_timestamp(3)", extra: "DEFAULT_GENERATED", check: func(d *ir.DefaultValue) bool {
			return d != nil && d.IsFunction && d.FuncName == "now"
		}},
		{name: "now function", def: "now()", extra: "DEFAULT_GENERATED", check: func(d *ir.DefaultValue) bool {
			return d != nil && d.IsFunction && d.FuncName == "now"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mysqlDefaultValue(tt.def, tt.extra); !tt.check(got) {
				t.Fatalf("mysqlDefaultValue(%q) = %#v", tt.def, got)
			}
		})
	}
}

func TestNormalizePostgresLiteralDefaultSkipsCastsInsideQuotes(t *testing.T) {
	tests := []struct {
		name string
		def  string
		want string
	}{
		{name: "plain cast", def: "'a'::text", want: "a"},
		{name: "cast operator inside literal", def: "'a::b'::text", want: "a::b"},
		{name: "escaped quote before cast operator", def: "'it''s::x'::text", want: "it's::x"},
		{name: "no cast", def: "'plain'", want: "plain"},
		{name: "numeric", def: "0", want: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePostgresLiteralDefault(tt.def); got != tt.want {
				t.Fatalf("normalizePostgresLiteralDefault(%q) = %q, want %q", tt.def, got, tt.want)
			}
		})
	}
}

func TestSQLiteTypeCompatibleWithTarget(t *testing.T) {
	field := func(scalar string, isList bool) *ir.Field {
		return &ir.Field{Type: ir.FieldKindScalar, ScalarType: scalar, IsList: isList}
	}

	tests := []struct {
		name     string
		dataType string
		target   *ir.Field
		want     bool
	}{
		{name: "nil target", dataType: "TEXT", target: nil, want: false},
		{name: "list accepts text", dataType: "TEXT", target: field("String", true), want: true},
		{name: "list rejects integer", dataType: "INTEGER", target: field("String", true), want: false},
		{name: "string accepts varchar", dataType: "VARCHAR(100)", target: field("String", false), want: true},
		{name: "string rejects integer", dataType: "INTEGER", target: field("String", false), want: false},
		{name: "int accepts integer", dataType: "INTEGER", target: field("Int", false), want: true},
		{name: "int rejects text", dataType: "TEXT", target: field("Int", false), want: false},
		{name: "boolean accepts boolean", dataType: "BOOLEAN", target: field("Boolean", false), want: true},
		{name: "float accepts real", dataType: "REAL", target: field("Float", false), want: true},
		{name: "float rejects int", dataType: "INTEGER", target: field("Float", false), want: false},
		{name: "bytes accepts blob", dataType: "BLOB", target: field("Bytes", false), want: true},
		{name: "bytes rejects text", dataType: "TEXT", target: field("Bytes", false), want: false},
		{name: "unknown scalar rejects", dataType: "TEXT", target: field("Custom", false), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sqliteTypeCompatibleWithTarget(tt.dataType, tt.target); got != tt.want {
				t.Fatalf("sqliteTypeCompatibleWithTarget(%q) = %v, want %v", tt.dataType, got, tt.want)
			}
		})
	}
}

func TestHasWrappingDefaultParens(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"()", true},
		{"(0)", true},
		{"((1))", true},
		{"(1) + (2)", false},
		{"(')')", true},
		{"(1", false},
		{"1", false},
	}
	for _, tt := range tests {
		if got := hasWrappingDefaultParens(tt.input); got != tt.want {
			t.Fatalf("hasWrappingDefaultParens(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestIsNumericLiteral(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"12.5", true},
		{"-3", true},
		{"1a", false},
		{"a1", false},
	}
	for _, tt := range tests {
		if got := isNumericLiteral(tt.input); got != tt.want {
			t.Fatalf("isNumericLiteral(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLiteralDefaultValue(t *testing.T) {
	tests := []struct {
		name  string
		def   string
		check func(*ir.DefaultValue) bool
	}{
		{name: "quoted string", def: "'draft'", check: func(d *ir.DefaultValue) bool { return d.IsString && d.Value == "draft" }},
		{name: "double quoted string", def: `"x"`, check: func(d *ir.DefaultValue) bool { return d.IsString && d.Value == "x" }},
		{name: "upper true", def: "TRUE", check: func(d *ir.DefaultValue) bool { return d.IsBool && d.Value == "true" }},
		{name: "zero is false", def: "0", check: func(d *ir.DefaultValue) bool { return d.IsBool && d.Value == "false" }},
		{name: "decimal number", def: "1.5", check: func(d *ir.DefaultValue) bool { return d.IsNumber && d.Value == "1.5" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := literalDefaultValue(tt.def); got == nil || !tt.check(got) {
				t.Fatalf("literalDefaultValue(%q) = %#v", tt.def, got)
			}
		})
	}
}

func TestFindFieldByColumn(t *testing.T) {
	model := &ir.Model{Fields: []*ir.Field{{Name: "email", DBName: "email_address"}}}

	if got := findFieldByColumn(model, "email_address"); got == nil || got.Name != "email" {
		t.Fatalf("findFieldByColumn(DBName) = %#v", got)
	}
	if got := findFieldByColumn(model, "email"); got == nil || got.Name != "email" {
		t.Fatalf("findFieldByColumn(Name) = %#v", got)
	}
	if got := findFieldByColumn(model, "missing"); got != nil {
		t.Fatalf("findFieldByColumn(missing) = %#v, want nil", got)
	}
}

func TestSQLiteFieldColumnName(t *testing.T) {
	if got := sqliteFieldColumnName(&ir.Field{Name: "email", DBName: "email_address"}); got != "email_address" {
		t.Fatalf("sqliteFieldColumnName() = %q", got)
	}
	if got := sqliteFieldColumnName(&ir.Field{Name: "createdAt"}); got != "created_at" {
		t.Fatalf("sqliteFieldColumnName() = %q", got)
	}
	if got := sqliteToSnakeCase("id"); got != "id" {
		t.Fatalf("sqliteToSnakeCase(id) = %q", got)
	}
}

func TestFindSQLiteTargetField(t *testing.T) {
	if got := findSQLiteTargetField(nil, "email"); got != nil {
		t.Fatalf("findSQLiteTargetField(nil model) = %#v, want nil", got)
	}

	model := &ir.Model{Fields: []*ir.Field{
		{Name: "author", Type: ir.FieldKindRelation},
		{Name: "email", Type: ir.FieldKindScalar},
	}}
	if got := findSQLiteTargetField(model, "author"); got != nil {
		t.Fatalf("findSQLiteTargetField(relation) = %#v, want nil", got)
	}
	if got := findSQLiteTargetField(model, "email"); got == nil || got.Name != "email" {
		t.Fatalf("findSQLiteTargetField() = %#v", got)
	}
}

func TestParseSQLiteColumnDef(t *testing.T) {
	tests := []struct {
		name     string
		def      string
		wantName string
	}{
		{name: "empty", def: "", wantName: ""},
		{name: "table constraint", def: "CONSTRAINT pk PRIMARY KEY (id)", wantName: ""},
		{name: "table primary key", def: "PRIMARY KEY (id)", wantName: ""},
		{name: "table foreign key", def: "FOREIGN KEY (author_id) REFERENCES users(id)", wantName: ""},
		{name: "table unique", def: "UNIQUE (email)", wantName: ""},
		{name: "table check", def: "CHECK (age > 0)", wantName: ""},
		{name: "double quoted", def: `"my col" INTEGER PRIMARY KEY`, wantName: "my col"},
		{name: "backtick quoted", def: "`bt` INT", wantName: "bt"},
		{name: "bracket quoted", def: "[br] INT", wantName: "br"},
		{name: "plain", def: "plain TEXT", wantName: "plain"},
		{name: "no type suffix", def: "noSpaces", wantName: "noSpaces"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, _ := parseSQLiteColumnDef(tt.def)
			if name != tt.wantName {
				t.Fatalf("parseSQLiteColumnDef(%q) name = %q, want %q", tt.def, name, tt.wantName)
			}
		})
	}
}

func TestReadDelimitedSQLiteIdentifier(t *testing.T) {
	t.Run("escaped quote", func(t *testing.T) {
		name, _ := readDelimitedSQLiteIdentifier(`"a""b" INTEGER`, '"', '"')
		if name != `a"b` {
			t.Fatalf("name = %q, want %q", name, `a"b`)
		}
	})

	t.Run("unterminated", func(t *testing.T) {
		name, _ := readDelimitedSQLiteIdentifier(`"abc`, '"', '"')
		if name != "" {
			t.Fatalf("name = %q, want empty", name)
		}
	})

	t.Run("mismatched opener", func(t *testing.T) {
		name, rest := readDelimitedSQLiteIdentifier("abc", '"', '"')
		if name != "" || rest != "abc" {
			t.Fatalf("name=%q rest=%q, want empty name and unchanged input", name, rest)
		}
	})
}

func TestSQLiteAutoIncrementColumns(t *testing.T) {
	t.Run("no column list", func(t *testing.T) {
		if got := sqliteAutoIncrementColumns(""); len(got) != 0 {
			t.Fatalf("sqliteAutoIncrementColumns() = %#v, want empty", got)
		}
	})

	t.Run("inline autoincrement only", func(t *testing.T) {
		createSQL := `CREATE TABLE t ("id" INTEGER PRIMARY KEY AUTOINCREMENT, "alt" INTEGER, PRIMARY KEY("alt"))`
		got := sqliteAutoIncrementColumns(createSQL)
		if !got["id"] || got["alt"] {
			t.Fatalf("sqliteAutoIncrementColumns() = %#v, want only id", got)
		}
	})
}

func TestApplyPostgresIndexCatalogOptions(t *testing.T) {
	t.Run("nil column is a no-op", func(t *testing.T) {
		applyPostgresIndexCatalogOptions(nil, "1", "text_ops", "en_US")
	})

	t.Run("nulls first without desc", func(t *testing.T) {
		col := &ir.IndexColumn{Field: "email"}
		applyPostgresIndexCatalogOptions(col, "2", "", "")
		if col.Sort != "ASC" || col.Nulls != "FIRST" {
			t.Fatalf("column = %+v, want ASC NULLS FIRST", col)
		}
	})

	t.Run("invalid option ignored", func(t *testing.T) {
		col := &ir.IndexColumn{Field: "email"}
		applyPostgresIndexCatalogOptions(col, "not-a-number", "", "")
		if col.Sort != "" || col.Nulls != "" {
			t.Fatalf("column = %+v, want no sort/nulls", col)
		}
	})

	t.Run("opclass and collation normalized", func(t *testing.T) {
		col := &ir.IndexColumn{Field: "email"}
		applyPostgresIndexCatalogOptions(col, "", `"text_ops"`, `"pg_catalog"."en_US"`)
		if col.OpClass != "text_ops" || col.Collation != "pg_catalog.en_US" {
			t.Fatalf("column = %+v", col)
		}
	})

	t.Run("NULL array items are cleared", func(t *testing.T) {
		if got := normalizePostgresArrayItem("NULL"); got != "" {
			t.Fatalf("normalizePostgresArrayItem(NULL) = %q", got)
		}
		if got := normalizePostgresArrayItem(`"text_ops"`); got != "text_ops" {
			t.Fatalf("normalizePostgresArrayItem() = %q", got)
		}
	})
}

func TestStripPostgresIndexExpressionOptions(t *testing.T) {
	got := stripPostgresIndexExpressionOptions(`lower(email) COLLATE "en_US" DESC`, ir.IndexColumn{Sort: "DESC", Collation: "en_US"})
	if got != "lower(email)" {
		t.Fatalf("stripPostgresIndexExpressionOptions() = %q, want %q", got, "lower(email)")
	}
}

func TestTrimPostgresIndexSuffix(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		suffix string
		want   string
	}{
		{name: "empty suffix", value: "col DESC", suffix: "", want: "col DESC"},
		{name: "matching suffix", value: "col DESC", suffix: "DESC", want: "col"},
		{name: "no space boundary", value: "colDESC", suffix: "DESC", want: "colDESC"},
		{name: "suffix longer than value", value: "col", suffix: "DESC", want: "col"},
		{name: "case insensitive", value: "col desc", suffix: "DESC", want: "col"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimPostgresIndexSuffix(tt.value, tt.suffix); got != tt.want {
				t.Fatalf("trimPostgresIndexSuffix(%q, %q) = %q, want %q", tt.value, tt.suffix, got, tt.want)
			}
		})
	}
}

func TestRunFlagParsing(t *testing.T) {
	t.Run("--url overrides schema datasource and --force is accepted", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "flag.db")
		schemaPath := filepath.Join(dir, "schema.gcorm")
		schema := `datasource db {
  provider = "sqlite"
  url      = env("GCO_TEST_UNSET_FLAG_URL")
}

model User {
  id    String @id
  email String @unique
}
`
		if err := os.WriteFile(schemaPath, []byte(schema), 0o644); err != nil {
			t.Fatal(err)
		}

		err := Run([]string{"--schema", schemaPath, "--url", "file:" + filepath.ToSlash(dbPath), "--force"})
		if err != nil {
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
			t.Fatalf("User table count = %d, want 1", count)
		}
	})

	t.Run("--config discovers schema roots", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)

		if err := os.MkdirAll(filepath.Join(dir, "schema"), 0o755); err != nil {
			t.Fatal(err)
		}
		schema := `datasource db {
  provider = "sqlite"
  url      = "file:` + filepath.ToSlash(filepath.Join(dir, "cfg.db")) + `"
}

model Widget {
  id String @id
}
`
		if err := os.WriteFile(filepath.Join(dir, "schema", "main.gcorm"), []byte(schema), 0o644); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(dir, "gco.config.yaml")
		if err := os.WriteFile(cfgPath, []byte("migrationDir: migrations\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := Run([]string{"--config", cfgPath}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "cfg.db")))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'Widget'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("Widget table count = %d, want 1", count)
		}
	})
}

func TestRunErrorPaths(t *testing.T) {
	t.Run("missing schema file", func(t *testing.T) {
		err := Run([]string{"--schema", filepath.Join(t.TempDir(), "does-not-exist.gcorm")})
		if err == nil {
			t.Fatal("Run() error = nil, want schema load error")
		}
	})

	t.Run("unreachable database", func(t *testing.T) {
		dir := t.TempDir()
		schemaPath := filepath.Join(dir, "schema.gcorm")
		schema := `datasource db {
  provider = "sqlite"
  url      = "file:` + filepath.ToSlash(filepath.Join(dir, "missing-dir", "dev.db")) + `"
}

model User {
  id String @id
}
`
		if err := os.WriteFile(schemaPath, []byte(schema), 0o644); err != nil {
			t.Fatal(err)
		}

		err := Run([]string{"--schema", schemaPath})
		if err == nil || !strings.Contains(err.Error(), "ping database") {
			t.Fatalf("Run() error = %v, want ping failure", err)
		}
	})

	t.Run("schema without connection URL", func(t *testing.T) {
		os.Unsetenv("GCO_TEST_UNSET_SCHEMA_URL")
		dir := t.TempDir()
		schemaPath := filepath.Join(dir, "schema.gcorm")
		schema := `datasource db {
  provider = "sqlite"
  url      = env("GCO_TEST_UNSET_SCHEMA_URL")
}

model User {
  id String @id
}
`
		if err := os.WriteFile(schemaPath, []byte(schema), 0o644); err != nil {
			t.Fatal(err)
		}

		err := Run([]string{"--schema", schemaPath})
		if err == nil || !strings.Contains(err.Error(), "GCO_TEST_UNSET_SCHEMA_URL is not set") {
			t.Fatalf("Run() error = %v, want missing URL error", err)
		}
	})
}

func TestPushInvalidExplicitURL(t *testing.T) {
	db, _ := openTestSQLite(t)

	schemaFS := fstest.MapFS{
		"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

model User {
  id String @id
}
`)},
	}

	_, err := Push(context.Background(), db, Options{
		SchemaFS:    schemaFS,
		SchemaRoot:  "schema",
		DatabaseURL: "postgresql://h%zz/db",
	})
	if err == nil || !strings.Contains(err.Error(), "parse postgresql connection URL") {
		t.Fatalf("Push() error = %v, want URL parse error", err)
	}
}

func TestResolveURLEnvVarUnset(t *testing.T) {
	os.Unsetenv("GCO_TEST_MISSING_DATABASE_URL")
	schema := &ir.Schema{
		Datasource: &ir.Datasource{Provider: "postgresql", URLIsEnv: true, EnvVar: "GCO_TEST_MISSING_DATABASE_URL"},
		Models:     []*ir.Model{{Name: "User"}},
	}

	_, _, err := resolveURL("", schema)
	if err == nil || !strings.Contains(err.Error(), "GCO_TEST_MISSING_DATABASE_URL is not set") {
		t.Fatalf("resolveURL() error = %v", err)
	}
}

func TestResolveURLEnvVarInvalidURL(t *testing.T) {
	t.Setenv("GCO_TEST_BAD_DATABASE_URL", "postgresql://h%zz/db")
	schema := &ir.Schema{
		Datasource: &ir.Datasource{Provider: "postgresql", URLIsEnv: true, EnvVar: "GCO_TEST_BAD_DATABASE_URL"},
	}

	_, _, err := resolveURL("", schema)
	if err == nil || !strings.Contains(err.Error(), "parse postgresql connection URL") {
		t.Fatalf("resolveURL() error = %v", err)
	}
}

func TestSplitStatementsUnterminatedQuotes(t *testing.T) {
	t.Run("unterminated dollar quote", func(t *testing.T) {
		stmts, unsupported := splitStatements("SELECT $$body; SELECT 1;", "postgresql")
		if len(unsupported) != 0 {
			t.Fatalf("len(unsupported) = %d, want 0: %v", len(unsupported), unsupported)
		}
		if len(stmts) != 1 || !strings.Contains(stmts[0], "$$body; SELECT 1") {
			t.Fatalf("stmts = %#v", stmts)
		}
	})

	t.Run("line comment at end of input", func(t *testing.T) {
		stmts, unsupported := splitStatements("SELECT 1 -- trailing", "sqlite")
		if len(stmts) != 1 || strings.TrimSpace(stmts[0]) != "SELECT 1" {
			t.Fatalf("stmts = %#v", stmts)
		}
		if len(unsupported) != 1 || unsupported[0] != "-- trailing" {
			t.Fatalf("unsupported = %#v", unsupported)
		}
	})
}

func TestStripTopLevelSQLCommentsUnterminatedBlock(t *testing.T) {
	cleaned, unsupported := stripTopLevelSQLComments("SELECT 1 /* never closed", "sqlite")
	if len(unsupported) != 0 {
		t.Fatalf("unsupported = %#v, want none", unsupported)
	}
	if strings.Contains(cleaned, "never closed") {
		t.Fatalf("cleaned kept block comment: %q", cleaned)
	}
}

func TestIntrospectSQLiteWithTargetRestoresListFields(t *testing.T) {
	db, _ := openTestSQLite(t)
	if _, err := db.Exec(`CREATE TABLE tags (id INTEGER PRIMARY KEY, vals TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	target := &ir.Schema{
		Datasource: &ir.Datasource{Provider: "sqlite"},
		Models: []*ir.Model{{
			Name: "tags",
			Fields: []*ir.Field{
				{Name: "id", Type: ir.FieldKindScalar, ScalarType: "Int", IsID: true},
				{Name: "vals", Type: ir.FieldKindScalar, ScalarType: "String", IsList: true},
			},
		}},
	}

	schema, err := introspectSQLiteWithTarget(context.Background(), db, target)
	if err != nil {
		t.Fatalf("introspectSQLiteWithTarget() error = %v", err)
	}
	model := findTestModel(schema, "tags")
	if model == nil {
		t.Fatal("tags model not found")
	}
	values := findFieldByColumn(model, "vals")
	if values == nil || !values.IsList || values.ScalarType != "String" {
		t.Fatalf("vals field = %#v, want String list restored from target", values)
	}
}

func TestIntrospectSQLiteIndexesEdgeCases(t *testing.T) {
	db, _ := openTestSQLite(t)
	statements := []string{
		`CREATE TABLE t (id INTEGER PRIMARY KEY, a INT, b INT, c TEXT)`,
		`CREATE UNIQUE INDEX uq_ab ON t(a, b)`,
		`CREATE INDEX idx_partial ON t(c) WHERE c IS NOT NULL`,
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
	model := findTestModel(schema, "t")
	if model == nil {
		t.Fatal("t model not found")
	}

	var composite *ir.UniqueConstraint
	for _, uc := range model.UniqueConstraints {
		if uc.Name == "uq_ab" {
			composite = uc
		}
	}
	if composite == nil || len(composite.Fields) != 2 {
		t.Fatalf("unique constraints = %#v, want uq_ab with 2 fields", model.UniqueConstraints)
	}

	var partial *ir.Index
	for _, idx := range model.Indexes {
		switch idx.Name {
		case "idx_partial":
			partial = idx
		}
	}
	if partial == nil || len(partial.Fields) != 1 || partial.Fields[0] != "c" {
		t.Fatalf("partial index = %#v, want idx_partial on c", partial)
	}
}

func TestPushSQLiteRejectsExpressionIndexes(t *testing.T) {
	for _, indexSQL := range []string{
		`CREATE INDEX idx_expr ON "User" (lower(email))`,
		`CREATE INDEX idx_expr ON "User" (tenant, lower(email))`,
		`CREATE UNIQUE INDEX idx_expr ON "User" (tenant, lower(email)) WHERE active = 1`,
	} {
		t.Run(indexSQL, func(t *testing.T) {
			db, dsn := openTestSQLite(t)
			opts := Options{
				SchemaFS: fstest.MapFS{"schema/main.gcorm": {Data: []byte(`datasource db {
  provider = "sqlite"
  url = "file:test.db"
}

model User {
  id Int @id
  tenant Int
  email String
  active Boolean
}
`)}},
				SchemaRoot:  "schema",
				DatabaseURL: dsn,
			}
			ctx := context.Background()
			if _, err := Push(ctx, db, opts); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(indexSQL); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO "User" (id, tenant, email, active) VALUES (1, 1, 'a@example.com', 0), (2, 1, 'A@example.com', 0)`); err != nil {
				t.Fatal(err)
			}
			_, err := Push(ctx, db, opts)
			if err == nil || !strings.Contains(err.Error(), `expression index "idx_expr"`) {
				t.Fatalf("Push() error = %v, want unsupported expression index diagnostic", err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM "User"`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("rows after repeated push: count=%d, error=%v", count, err)
			}
			var storedSQL string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_expr'`).Scan(&storedSQL); err != nil || storedSQL != indexSQL {
				t.Fatalf("index after rejected push: sql=%q, error=%v", storedSQL, err)
			}
		})
	}
}

func TestParsePostgresTextArrayEscapedQuotes(t *testing.T) {
	got := parsePostgresTextArray(`{"a\"b",c}`)
	if len(got) != 2 || got[0] != `a"b` || got[1] != "c" {
		t.Fatalf("parsePostgresTextArray() = %#v", got)
	}
}

func TestSplitPostgresArrayDefaultElementsNestedParens(t *testing.T) {
	parts := splitPostgresArrayDefaultElements("(a,(b,c)), d")
	if len(parts) != 2 || strings.TrimSpace(parts[1]) != "d" {
		t.Fatalf("splitPostgresArrayDefaultElements() = %#v", parts)
	}
}
