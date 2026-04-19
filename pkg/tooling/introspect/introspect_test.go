package introspect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		`@@index([age])`,
	}

	for _, c := range checks {
		if !strings.Contains(schema, c) {
			t.Errorf("generated schema missing %q\nGot:\n%s", c, schema)
		}
	}
}

func TestWriteSchemaFile(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "schema")
	content := "datasource db {}\n"

	if err := WriteSchemaFile(subdir, "main.gco", content); err != nil {
		t.Fatalf("WriteSchemaFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(subdir, "main.gco"))
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
		{"42", `"42"`},
	}
	for _, tt := range tests {
		got := mapDefault(tt.input)
		if got != tt.want {
			t.Errorf("mapDefault(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
