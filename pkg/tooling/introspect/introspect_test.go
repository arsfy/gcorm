package introspect

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/ir"
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

func TestGenerateSchemaPreservesConstantExpressionIndex(t *testing.T) {
	tables := []TableInfo{{
		Name:    "events",
		Columns: []ColumnInfo{{Name: "id", DataType: "INT", IsPrimaryKey: true}},
		Indexes: []IndexInfo{{
			Name:        "idx_events_constant",
			Columns:     []string{""},
			Expressions: []string{"1"},
		}},
	}}

	schema := GenerateSchema("postgresql", tables)
	want := `@@index([id], name: "idx_events_constant", expression: "1")`
	if !strings.Contains(schema, want) {
		t.Fatalf("constant expression index was not preserved:\n%s", schema)
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

func TestIntrospectDriver(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"postgresql", "pgx"},
		{"sqlite", "sqlite"},
		{"mysql", "mysql"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := introspectDriver(tt.provider); got != tt.want {
				t.Errorf("introspectDriver(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestTablesFromIRNil(t *testing.T) {
	if got := tablesFromIR(nil); got != nil {
		t.Errorf("tablesFromIR(nil) = %#v, want nil", got)
	}
}

func TestTablesFromIR(t *testing.T) {
	schema := &ir.Schema{Models: []*ir.Model{
		{
			Name:   "User",
			DBName: "users",
			Schema: "app",
			Fields: []*ir.Field{
				{Name: "id", ScalarType: "Int", IsID: true},
				{Name: "tenant", DBName: "tenant_id", ScalarType: "Int"},
				{Name: "email", ScalarType: "String", IsUnique: true},
				{Name: "createdAt", ScalarType: "DateTime", IsOptional: true, Default: &ir.DefaultValue{IsFunction: true, FuncName: "now"}},
				{Name: "age", ScalarType: "Int", Default: &ir.DefaultValue{Value: "18"}},
				{Name: "posts", Type: ir.FieldKindRelation, ModelType: "Post"},
			},
			PrimaryKey: &ir.PrimaryKey{Fields: []string{"id", "tenant_id"}, IsComposite: true},
			Indexes: []*ir.Index{
				{Name: "idx_age", Fields: []string{"age"}},
				{Name: "idx_email_expr", Columns: []ir.IndexColumn{{Field: "email", Expression: "lower(email)"}}, IsUnique: true},
			},
			UniqueConstraints: []*ir.UniqueConstraint{
				{Name: "uq_email_age", Fields: []string{"email", "age"}},
			},
			Relations: []*ir.Relation{
				{ConstraintName: "fk_age", Fields: []string{"age"}, ToModel: "Post", References: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "SET NULL"},
				{ToModel: "Post"}, // no fields: must be skipped
			},
		},
	}}

	tables := tablesFromIR(schema)
	if len(tables) != 1 {
		t.Fatalf("tablesFromIR returned %d tables, want 1", len(tables))
	}
	tb := tables[0]
	if tb.Name != "users" || tb.Schema != "app" {
		t.Fatalf("table = %#v", tb)
	}
	if len(tb.Columns) != 5 {
		t.Fatalf("columns = %#v", tb.Columns)
	}
	id, tenant, email, createdAt, age := tb.Columns[0], tb.Columns[1], tb.Columns[2], tb.Columns[3], tb.Columns[4]
	if !id.IsPrimaryKey {
		t.Errorf("id.IsPrimaryKey = false, want true")
	}
	if tenant.Name != "tenant_id" || !tenant.IsPrimaryKey {
		t.Errorf("tenant column = %#v, want DBName mapped and primary key via pk list", tenant)
	}
	if !email.IsUnique {
		t.Errorf("email.IsUnique = false, want true")
	}
	if !createdAt.IsNullable || createdAt.Default != "now()" {
		t.Errorf("createdAt column = %#v, want nullable with now() default", createdAt)
	}
	if age.Default != "18" {
		t.Errorf("age.Default = %q, want %q", age.Default, "18")
	}

	if len(tb.Indexes) != 3 {
		t.Fatalf("indexes = %#v", tb.Indexes)
	}
	if tb.Indexes[0].Name != "idx_age" || tb.Indexes[0].IsUnique {
		t.Errorf("index[0] = %#v", tb.Indexes[0])
	}
	exprIdx := tb.Indexes[1]
	if exprIdx.Name != "idx_email_expr" || !exprIdx.IsUnique ||
		len(exprIdx.Columns) != 1 || exprIdx.Columns[0] != "email" ||
		len(exprIdx.Expressions) != 1 || exprIdx.Expressions[0] != "lower(email)" {
		t.Errorf("expression index = %#v", exprIdx)
	}
	uc := tb.Indexes[2]
	if uc.Name != "uq_email_age" || !uc.IsUnique || len(uc.Columns) != 2 {
		t.Errorf("unique constraint index = %#v", uc)
	}

	if len(tb.FKs) != 1 {
		t.Fatalf("fks = %#v", tb.FKs)
	}
	fk := tb.FKs[0]
	if fk.Name != "fk_age" || fk.RefTable != "Post" || fk.OnDelete != "CASCADE" || fk.OnUpdate != "SET NULL" ||
		len(fk.Columns) != 1 || fk.Columns[0] != "age" || len(fk.RefColumns) != 1 || fk.RefColumns[0] != "id" {
		t.Errorf("fk = %#v", fk)
	}
}

func TestRunWithSQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			name TEXT,
			created_at DATETIME DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE posts (
			id INTEGER PRIMARY KEY,
			title TEXT NOT NULL,
			author_id INTEGER NOT NULL,
			FOREIGN KEY(author_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX idx_posts_title ON posts(title)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	if err := Run([]string{"--url", dbPath, "--output", outDir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "schema.gcorm"))
	if err != nil {
		t.Fatalf("read generated schema: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		`provider = "sqlite"`,
		`model Users {`,
		`model Posts {`,
		`@relation(fields: [author_id], references: [id]`,
		`onDelete: Cascade`,
		`@@index([title], name: "idx_posts_title")`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("generated schema missing %q\nGot:\n%s", want, content)
		}
	}
	if result := compiler.CompileFile("schema.gcorm", data); result.HasErrors() {
		t.Fatalf("generated schema invalid: %v\n%s", result.Errors, content)
	}
}

func TestRunConnectError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run([]string{"--url", filepath.Join(blocker, "x.db")})
	if err == nil {
		t.Fatal("expected error for unreachable database")
	}
	if !strings.Contains(err.Error(), "connect database") {
		t.Fatalf("error = %v, want connect database failure", err)
	}
}

func TestRunInvalidURL(t *testing.T) {
	err := Run([]string{"--url", "postgresql://exa mple/db", "--provider", "postgresql"})
	if err == nil {
		t.Fatal("expected error for invalid connection URL")
	}
}

func TestGenerateSchemaWithRelations(t *testing.T) {
	tables := []TableInfo{
		{
			Name:    "users",
			Columns: []ColumnInfo{{Name: "id", DataType: "INT", IsPrimaryKey: true}},
		},
		{
			Name: "posts",
			Columns: []ColumnInfo{
				{Name: "id", DataType: "INT", IsPrimaryKey: true},
				{Name: "author_id", DataType: "INT"},
				{Name: "orphan_id", DataType: "INT"},
			},
			FKs: []ForeignKeyInfo{
				{Name: "fk_author", Columns: []string{"author_id"}, RefTable: "users", RefColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "SET NULL"},
				// Unknown referenced table: refModel falls back to a sanitized name,
				// then the relation is skipped because reference columns cannot be mapped.
				{Columns: []string{"orphan_id"}, RefTable: "missing_table", RefColumns: []string{"id"}},
				// Mismatched column counts: relation is skipped.
				{Columns: []string{"author_id"}, RefTable: "users"},
			},
		},
	}

	schema := GenerateSchema("postgresql", tables)
	want := `Users @relation(fields: [author_id], references: [id], name: "fk_author", onDelete: Cascade, onUpdate: SetNull)`
	if !strings.Contains(schema, want) {
		t.Fatalf("missing relation line %q:\n%s", want, schema)
	}
	if strings.Contains(schema, "orphan_id]") || strings.Contains(schema, "MissingTable @relation") {
		t.Fatalf("unmappable relations should have been omitted:\n%s", schema)
	}
}

func TestGenerateSchemaOmitsUnrepresentableIndex(t *testing.T) {
	tables := []TableInfo{{
		Name:    "events",
		Columns: []ColumnInfo{{Name: "id", DataType: "INT", IsPrimaryKey: true}},
		Indexes: []IndexInfo{{Name: "idx_bad", Columns: []string{"no_such_column"}}},
	}}

	schema := GenerateSchema("postgresql", tables)
	want := `// WARNING: index "idx_bad" omitted because its columns could not be represented`
	if !strings.Contains(schema, want) {
		t.Fatalf("missing warning comment:\n%s", schema)
	}
}

func TestGenerateSchemaWithMultiExpressionIndex(t *testing.T) {
	tables := []TableInfo{{
		Name: "docs",
		Columns: []ColumnInfo{
			{Name: "id", DataType: "INT", IsPrimaryKey: true},
			{Name: "a", DataType: "TEXT"},
			{Name: "b", DataType: "TEXT"},
		},
		Indexes: []IndexInfo{{
			Name:        "idx_docs_expr",
			Columns:     []string{"", ""},
			Expressions: []string{"lower(a)", "upper(b)"},
		}},
	}}

	schema := GenerateSchema("postgresql", tables)
	want := `@@index([a, b], name: "idx_docs_expr", expressions: ["lower(a)", "upper(b)"])`
	if !strings.Contains(schema, want) {
		t.Fatalf("missing multi-expression index:\n%s", schema)
	}
}

func TestGenerateSchemaWithSchemaNamespace(t *testing.T) {
	tables := []TableInfo{{
		Name:    "users",
		Schema:  "app",
		Columns: []ColumnInfo{{Name: "id", DataType: "INT", IsPrimaryKey: true}},
	}}

	schema := GenerateSchema("postgresql", tables)
	if !strings.Contains(schema, `@@schema("app")`) {
		t.Fatalf("missing @@schema:\n%s", schema)
	}
}

func TestReferentialAction(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"CASCADE", "Cascade"},
		{"cascade", "Cascade"},
		{"RESTRICT", "Restrict"},
		{"SET NULL", "SetNull"},
		{"set null", "SetNull"},
		{"SET DEFAULT", "SetDefault"},
		{"NO ACTION", "NoAction"},
		{"", "NoAction"},
		{"something-else", "NoAction"},
	}
	for _, tt := range tests {
		t.Run(tt.want+"/"+tt.input, func(t *testing.T) {
			if got := referentialAction(tt.input); got != tt.want {
				t.Errorf("referentialAction(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatIndexExpressions(t *testing.T) {
	if got := formatIndexExpressions([]string{"lower(email)"}); got != `expression: "lower(email)"` {
		t.Errorf("single expression = %q", got)
	}
	if got := formatIndexExpressions([]string{"lower(a)", "upper(b)"}); got != `expressions: ["lower(a)", "upper(b)"]` {
		t.Errorf("multiple expressions = %q", got)
	}
	if got := formatIndexExpressions(nil); got != `expressions: []` {
		t.Errorf("empty expressions = %q", got)
	}
}

func TestWriteSchemaFileErrors(t *testing.T) {
	dir := t.TempDir()

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSchemaFile(filepath.Join(blocker, "sub"), "schema.gcorm", "content"); err == nil {
		t.Error("expected error when output dir cannot be created")
	}

	subdir := filepath.Join(dir, "out", "schema.gcorm")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteSchemaFile(filepath.Join(dir, "out"), "schema.gcorm", "content"); err == nil {
		t.Error("expected error when schema file path is a directory")
	}
}

func TestIsPostgresCastType(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"[]", false},
		{"text", true},
		{"text[]", true},
		{"character varying", true},
		{"timestamp with time zone", true},
		{"numeric(10,2)", true},
		{"varchar()", false},
		{"varchar(abc)", false},
		{"some thing", false},
		{"te!xt", false},
		{"my_schema.my_type", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isPostgresCastType(tt.input); got != tt.want {
				t.Errorf("isPostgresCastType(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestUnquoteSQLDefault(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"a", "a"},
		{"'abc'", "abc"},
		{"'O''Reilly'", "O'Reilly"},
		{`"a""b"`, `a"b`},
		{"'abc", "'abc"},
		{`"abc`, `"abc`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := unquoteSQLDefault(tt.input); got != tt.want {
				t.Errorf("unquoteSQLDefault(%q) = %q, want %q", tt.input, got, tt.want)
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
		{"(", false},
		{"()", true},
		{"(a)", true},
		{"((a))", true},
		{"(a)+(b)", false},
		{"(a", false},
		{"a)", false},
		{"(')')", true},
		{"('a'')')", true},
		{`("(")`, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := hasWrappingDefaultParens(tt.input); got != tt.want {
				t.Errorf("hasWrappingDefaultParens(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeNameFallbacks(t *testing.T) {
	if got := sanitizeModelName(""); got != "Model" {
		t.Errorf(`sanitizeModelName("") = %q, want "Model"`, got)
	}
	if got := sanitizeFieldName(""); got != "field" {
		t.Errorf(`sanitizeFieldName("") = %q, want "field"`, got)
	}
	if got := sanitizeModelName("123"); got != "Model123" {
		t.Errorf(`sanitizeModelName("123") = %q, want "Model123"`, got)
	}
	if got := sanitizeFieldName("1st"); got != "field1st" {
		t.Errorf(`sanitizeFieldName("1st") = %q, want "field1st"`, got)
	}
	if got := sanitizeFieldName("Name"); got != "name" {
		t.Errorf(`sanitizeFieldName("Name") = %q, want "name"`, got)
	}
}

func TestPostgresExpressionReferencesColumn(t *testing.T) {
	tests := []struct {
		expression string
		column     string
		want       bool
	}{
		{"lower(email)", "email", true},
		{"lower(email)", "mail", false},
		{`lower("email")`, "email", true},
		{`lower("em""ail")`, `em"ail`, true},
		{"email_address", "email", false},
		{"email", "email", true},
		{"", "email", false},
	}
	for _, tt := range tests {
		if got := postgresExpressionReferencesColumn(tt.expression, tt.column); got != tt.want {
			t.Errorf("postgresExpressionReferencesColumn(%q, %q) = %v, want %v", tt.expression, tt.column, got, tt.want)
		}
	}
}

func TestMapDefaultForType(t *testing.T) {
	tests := []struct {
		def       string
		fieldType string
		want      string
	}{
		{"1", "Boolean", "true"},
		{"TRUE", "Boolean", "true"},
		{"0", "Boolean", "false"},
		{"false", "Boolean", "false"},
		{"'t'", "Boolean", `"t"`},
		{"-3.5", "Float", "-3.5"},
		{"abc", "Int", `"abc"`},
		{"(1 + 2)", "", `"1 + 2"`},
		{"AUTOINCREMENT", "", "autoincrement()"},
	}
	for _, tt := range tests {
		got := mapDefaultForType(tt.def, tt.fieldType)
		if got != tt.want {
			t.Errorf("mapDefaultForType(%q, %q) = %q, want %q", tt.def, tt.fieldType, got, tt.want)
		}
	}
}

func TestIsSQLStringLiteral(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"'", false},
		{"'abc'", true},
		{"'a''b'", true},
		{"'abc'::text", true},
		{"'abc' junk", false},
		{`"abc"`, true},
		{"abc", false},
		{"'a", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isSQLStringLiteral(tt.input); got != tt.want {
				t.Errorf("isSQLStringLiteral(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMapSQLTypeAdditional(t *testing.T) {
	tests := []struct {
		provider string
		sqlType  string
		want     string
	}{
		{"mysql", "CHAR(36)", "UUID"},
		{"postgresql", "BIGSERIAL", "BigInt"},
		{"postgresql", "SERIAL", "Int"},
		{"postgresql", "TINYINT(1)", "Int"}, // Boolean mapping is mysql-only
		{"postgresql", "FLOAT8", "Float"},
		{"postgresql", "REAL", "Float"},
		{"postgresql", "NUMERIC(12,4)", "Decimal"},
		{"postgresql", "DATE", "DateTime"},
		{"mysql", "VARBINARY(16)", "Bytes"},
		{"postgresql", "JSON", "Json"},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"_"+tt.sqlType, func(t *testing.T) {
			if got := mapSQLType(tt.provider, tt.sqlType); got != tt.want {
				t.Errorf("mapSQLType(%q, %q) = %q, want %q", tt.provider, tt.sqlType, got, tt.want)
			}
		})
	}
}
