package introspect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/parser"
)

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		dsn  string
		want string
	}{
		{"postgresql://localhost:5432/db", "postgresql"},
		{"postgres://user:pass@host/db", "postgresql"},
		{"mysql://user:pass@tcp(host)/db", "mysql"},
		{"user:pass@tcp(host:3306)/db", "mysql"},
		{"sqlite:///path/to/db.sqlite", "sqlite"},
		{"file:test.db", "sqlite"},
		{"/path/to/data.db", "sqlite"},
		{"unknown://foo", "postgresql"}, // default
	}

	for _, tt := range tests {
		t.Run(tt.dsn, func(t *testing.T) {
			got := DetectProvider(tt.dsn)
			if got != tt.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}

func TestRunRequiresURL(t *testing.T) {
	err := Run(nil)
	if err == nil {
		t.Fatal("expected error when --url is missing")
	}
}

func TestMapSQLType(t *testing.T) {
	tests := []struct {
		provider string
		sqlType  string
		want     string
	}{
		{"postgresql", "UUID", "UUID"},
		{"postgresql", "BIGINT", "BigInt"},
		{"postgresql", "SMALLINT", "SmallInt"},
		{"postgresql", "INT2", "SmallInt"},
		{"postgresql", "INTEGER", "Int"},
		{"postgresql", "DOUBLE PRECISION", "Float"},
		{"postgresql", "DECIMAL(10,2)", "Decimal"},
		{"postgresql", "BOOLEAN", "Boolean"},
		{"postgresql", "TIMESTAMPTZ", "DateTime"},
		{"postgresql", "BYTEA", "Bytes"},
		{"postgresql", "JSONB", "Json"},
		{"postgresql", "TEXT", "String"},
		{"postgresql", "VARCHAR(255)", "String"},
		{"mysql", "TINYINT(1)", "Boolean"},
		{"mysql", "SMALLINT", "SmallInt"},
		{"mysql", "INT", "Int"},
		{"mysql", "BIGINT", "BigInt"},
		{"mysql", "DATETIME(3)", "DateTime"},
		{"mysql", "JSON", "Json"},
		{"mysql", "LONGBLOB", "Bytes"},
		{"sqlite", "INTEGER", "Int"},
		{"sqlite", "REAL", "Float"},
		{"sqlite", "TEXT", "String"},
		{"sqlite", "BLOB", "Bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"_"+tt.sqlType, func(t *testing.T) {
			got := mapSQLType(tt.provider, tt.sqlType)
			if got != tt.want {
				t.Errorf("mapSQLType(%q, %q) = %q, want %q", tt.provider, tt.sqlType, got, tt.want)
			}
		})
	}
}

func TestGenerateSchema(t *testing.T) {
	tables := []TableInfo{
		{
			Name: "users",
			Columns: []ColumnInfo{
				{Name: "id", DataType: "UUID", IsPrimaryKey: true, Default: "gen_random_uuid()"},
				{Name: "email", DataType: "VARCHAR(255)", IsUnique: true},
				{Name: "name", DataType: "TEXT", IsNullable: true},
				{Name: "age", DataType: "INTEGER"},
				{Name: "created_at", DataType: "TIMESTAMPTZ", Default: "now()"},
			},
			Indexes: []IndexInfo{
				{Name: "idx_users_age", Columns: []string{"age"}},
			},
		},
		{
			Name: "posts",
			Columns: []ColumnInfo{
				{Name: "id", DataType: "BIGSERIAL", IsPrimaryKey: true},
				{Name: "title", DataType: "TEXT"},
				{Name: "body", DataType: "TEXT", IsNullable: true},
				{Name: "published", DataType: "BOOLEAN", Default: "false"},
			},
		},
	}

	schema := GenerateSchema("postgresql", tables)

	checks := []string{
		`datasource db {`,
		`provider = "postgresql"`,
		`env("DATABASE_URL")`,
		`model Users {`,
		`@id`,
		`@default(uuid())`,
		`@unique`,
		`model Posts {`,
		`@@index([age], name: "idx_users_age")`,
		`@@map("users")`,
		`@@map("posts")`,
	}

	for _, c := range checks {
		if !strings.Contains(schema, c) {
			t.Errorf("generated schema missing %q\nGot:\n%s", c, schema)
		}
	}
}

func TestGenerateSchemaWithExpressionUniqueIndex(t *testing.T) {
	tables := []TableInfo{{
		Name:    "users",
		Columns: []ColumnInfo{{Name: "id", DataType: "INT", IsPrimaryKey: true}, {Name: "email", DataType: "TEXT"}},
		Indexes: []IndexInfo{{
			Name:        "uq_users_email_ci",
			Columns:     []string{""},
			Expressions: []string{"lower(email)"},
			IsUnique:    true,
		}},
	}}

	schema := GenerateSchema("postgresql", tables)
	want := `@@unique([email], name: "uq_users_email_ci", expression: "lower(email)")`
	if !strings.Contains(schema, want) {
		t.Fatalf("missing expression unique index:\n%s", schema)
	}
}

func TestGenerateSchemaSanitizesNamesAndPreservesCompositeConstraints(t *testing.T) {
	tables := []TableInfo{{Name: "user-data", Columns: []ColumnInfo{{Name: "1st", DataType: "INT", IsPrimaryKey: true}, {Name: "User ID", DataType: "INT", IsPrimaryKey: true}, {Name: "user-id", DataType: "TEXT"}}, Indexes: []IndexInfo{{Name: "uq_user", Columns: []string{"User ID", "user-id"}, IsUnique: true}}}}
	schema := GenerateSchema("sqlite", tables)
	for _, want := range []string{`model UserData {`, `field1st`, `userID`, `userId`, `@map("1st")`, `@@id([field1st, userID])`, `@@unique([userID, userId], name: "uq_user")`, `@@map("user-data")`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("missing %q:\n%s", want, schema)
		}
	}
	if result := compiler.CompileFile("schema.gcorm", []byte(schema)); result.HasErrors() {
		t.Fatalf("generated schema invalid: %v\n%s", result.Errors, schema)
	}
}

func TestGenerateSchemaPreservesTableMappingAndTypedDefaults(t *testing.T) {
	tables := []TableInfo{{
		Name: "account_settings",
		Columns: []ColumnInfo{
			{Name: "id", DataType: "INTEGER", IsPrimaryKey: true, Default: "42"},
			{Name: "enabled", DataType: "BOOLEAN", Default: "false"},
			{Name: "attempts", DataType: "INTEGER", Default: "(3)"},
			{Name: "label", DataType: "TEXT", Default: "'O''Reilly'::text"},
			{Name: "mode", DataType: "TEXT", Default: "'uuid'::text"},
		},
	}}

	schema := GenerateSchema("postgresql", tables)
	for _, want := range []string{
		`model AccountSettings {`,
		`@@map("account_settings")`,
		`@default(42)`,
		`@default(false)`,
		`@default(3)`,
		`@default("O'Reilly")`,
		`@default("uuid")`,
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("generated schema missing %q:\n%s", want, schema)
		}
	}
	if strings.Contains(schema, `@default(uuid())`) {
		t.Fatalf("quoted uuid literal was mistaken for uuid():\n%s", schema)
	}

	ds, err := parser.ParseMulti(map[string][]byte{"schema.gcorm": []byte(schema)})
	if err != nil {
		t.Fatalf("generated schema parse error: %v\n%s", err, schema)
	}
	result := compiler.Compile(ds)
	if len(result.Errors) > 0 {
		t.Fatalf("generated schema compile errors: %v\n%s", result.Errors, schema)
	}
	if result.Schema == nil || len(result.Schema.Models) != 1 {
		t.Fatalf("compiled generated schema = %#v", result.Schema)
	}
	if got := result.Schema.Models[0].TableName(); got != "account_settings" {
		t.Fatalf("compiled table name = %q, want account_settings", got)
	}
}

func TestWriteSchemaFile(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "schema")
	content := "datasource db {}\n"

	if err := WriteSchemaFile(subdir, "main.gcorm", content); err != nil {
		t.Fatalf("WriteSchemaFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(subdir, "main.gcorm"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != content {
		t.Errorf("content mismatch: got %q, want %q", string(data), content)
	}
}

func TestPascalCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"users", "Users"},
		{"user_roles", "UserRoles"},
		{"my-table", "MyTable"},
		{"simple", "Simple"},
	}
	for _, tt := range tests {
		got := pascalCase(tt.input)
		if got != tt.want {
			t.Errorf("pascalCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMapDefault(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gen_random_uuid()", "uuid()"},
		{"now()", "now()"},
		{"CURRENT_TIMESTAMP", "now()"},
		{"nextval('seq')", "autoincrement()"},
		{"42", `42`},
		{"false", `false`},
		{"'draft'::text", `"draft"`},
		{"'uuid'::text", `"uuid"`},
	}
	for _, tt := range tests {
		got := mapDefault(tt.input)
		if got != tt.want {
			t.Errorf("mapDefault(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNumericDefaultMatchesSchemaNumberGrammar(t *testing.T) {
	for _, value := range []string{"0", "42", "-7", "3.14", "-0.5"} {
		if !isNumericDefault(value) {
			t.Errorf("isNumericDefault(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "+1", "1e3", "NaN", ".5", "1.", "--1"} {
		if isNumericDefault(value) {
			t.Errorf("isNumericDefault(%q) = true, want false", value)
		}
	}
}

func TestStripPostgresDefaultCastOnlyRemovesTrailingCast(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: `'draft'::text`, want: `'draft'`},
		{input: `'42'::text::integer`, want: `'42'`},
		{input: `(1 + 2)::numeric`, want: `(1 + 2)`},
		{input: `coalesce('a'::text, 'b')`, want: `coalesce('a'::text, 'b')`},
		{input: `price::numeric + 1`, want: `price::numeric + 1`},
	}
	for _, tt := range tests {
		if got := stripPostgresDefaultCast(tt.input); got != tt.want {
			t.Errorf("stripPostgresDefaultCast(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
