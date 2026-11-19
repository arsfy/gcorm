package introspect

import (
	"regexp"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/ir"
)

func introspectedIRFixture() *ir.Schema {
	users := &ir.Model{
		Name: "users", DBName: "users",
		Fields: []*ir.Field{
			{Name: "id", DBName: "id", ScalarType: "Int", IsID: true, Default: &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}},
			{Name: "email", DBName: "email", ScalarType: "String", IsUnique: true, NativeType: &ir.NativeType{Name: "VarChar", Args: []string{"20"}}},
			{Name: "avatar", DBName: "avatar", ScalarType: "Bytes", IsOptional: true},
			{Name: "tags", DBName: "tags", ScalarType: "String", IsList: true, Default: &ir.DefaultValue{IsArray: true, ArrayValue: []string{"a", "b c"}}},
			{Name: "scores", DBName: "scores", ScalarType: "Int", IsList: true, IsOptional: true},
			{Name: "balance", DBName: "balance", ScalarType: "Decimal", NativeType: &ir.NativeType{Name: "Decimal", Args: []string{"10", "2"}}, Default: &ir.DefaultValue{Value: "0", IsNumber: true}},
			{Name: "seen_at", DBName: "seen_at", ScalarType: "DateTime", IsOptional: true, NativeType: &ir.NativeType{Name: "Timestamp", Args: []string{"6"}}},
			{Name: "meta", DBName: "meta", ScalarType: "Json", IsOptional: true},
			{Name: "external_id", DBName: "external_id", ScalarType: "UUID", Default: &ir.DefaultValue{IsFunction: true, FuncName: "uuid"}},
			{Name: "token", DBName: "token", ScalarType: "String", Default: &ir.DefaultValue{Value: "uuid-v1", IsString: true}},
			{Name: "stamp", DBName: "stamp", ScalarType: "String", Default: &ir.DefaultValue{Value: "now()", IsString: true}},
			{Name: "code", DBName: "code", ScalarType: "String", Default: &ir.DefaultValue{Value: "42", IsNumber: true}},
			{Name: "created_text", DBName: "created_text", ScalarType: "String", Default: &ir.DefaultValue{IsFunction: true, FuncName: "now"}},
			{Name: "slug", DBName: "slug", ScalarType: "String", Default: &ir.DefaultValue{IsFunction: true, FuncName: "dbgenerated", FuncArgs: []string{"md5(random()::text)"}}},
			{Name: "status", DBName: "status", Type: ir.FieldKindEnum, EnumType: "order_status", Default: &ir.DefaultValue{Value: "in progress", IsString: true}},
			{Name: "email_lower", DBName: "email_lower", ScalarType: "String", IsOptional: true, IsGenerated: true, Default: &ir.DefaultValue{IsFunction: true, FuncName: "dbgenerated", FuncArgs: []string{`lower((email)::text)`}}},
		},
		PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
		Indexes: []*ir.Index{
			{Name: "users_seen_idx", Fields: []string{"seen_at", "email"}, Columns: []ir.IndexColumn{
				{Field: "seen_at", Sort: "DESC", Nulls: "LAST"},
				{Field: "email", OpClass: "varchar_pattern_ops", Collation: "C"},
			}},
			{Name: "users_email_desc_key", IsUnique: true, Fields: []string{"email"}, Columns: []ir.IndexColumn{{Field: "email", Sort: "DESC"}}},
		},
	}
	posts := &ir.Model{
		Name: "posts", DBName: "posts",
		Fields: []*ir.Field{
			{Name: "id", DBName: "id", ScalarType: "Int", IsID: true, Default: &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}},
			{Name: "author_id", DBName: "author_id", ScalarType: "Int"},
			{Name: "editorId", DBName: "editorId", ScalarType: "Int", IsOptional: true},
			{Name: "parent_id", DBName: "parent_id", ScalarType: "Int", IsOptional: true},
		},
		PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
		Relations: []*ir.Relation{
			{ConstraintName: "posts_author_fkey", Fields: []string{"author_id"}, ToModel: "users", References: []string{"id"}},
			{ConstraintName: "posts_editor_fkey", Fields: []string{"editorId"}, ToModel: "users", References: []string{"id"}},
			{ConstraintName: "posts_parent_fkey", Fields: []string{"parent_id"}, ToModel: "posts", References: []string{"id"}},
		},
	}
	return &ir.Schema{
		Models: []*ir.Model{posts, users},
		Enums: []*ir.Enum{{Name: "order_status", Values: []*ir.EnumValue{
			{Name: "pending"}, {Name: "in progress"}, {Name: "1st"}, {Name: "true"}, {Name: "DONE"},
		}}},
	}
}

func TestGenerateSchemaFromIRCarriesFullFields(t *testing.T) {
	schema := introspectedIRFixture()
	content := GenerateSchemaWithEnums("postgresql", tablesFromIR(schema), enumsFromIR(schema))

	result := compiler.CompileFile("schema.gcorm", []byte(content))
	if result.HasErrors() {
		t.Fatalf("generated schema does not compile: %v\n%s", result.Errors, content)
	}

	for _, want := range []string{
		"enum OrderStatus {\n  pending\n  in_progress @map(\"in progress\")\n  V_1st @map(\"1st\")\n  true_value @map(\"true\")\n  DONE\n\n  @@map(\"order_status\")\n}",
		`Int @id @default(autoincrement())`,
		`String @unique @db.VarChar(20)`,
		`Bytes?`,
		`String[] @default(["a", "b c"])`,
		`scores               Int[]`,
		`Decimal @default(0) @db.Decimal(10, 2)`,
		`DateTime? @db.Timestamp(6)`,
		`meta                 Json?`,
		`UUID @default(uuid())`,
		`token                String @default("uuid-v1")`,
		`stamp                String @default("now()")`,
		`code                 String @default("42")`,
		`created_text         String @default(dbgenerated("NOW()"))`,
		`slug                 String @default(dbgenerated("md5(random()::text)"))`,
		`status               OrderStatus @default(in_progress)`,
		"// email_lower is a read-only generated column: GENERATED ALWAYS AS (lower((email)::text)) STORED\n  email_lower          String? @default(dbgenerated(\"lower((email)::text)\"))",
		`@@index([seen_at, email], name: "users_seen_idx", sort: ["DESC", ""], nulls: ["LAST", ""], opclass: ["", "varchar_pattern_ops"], collate: ["", "C"])`,
		`// WARNING: sort/nulls/opclass/collation options of unique index "users_email_desc_key" are not representable`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("generated schema missing %q\n%s", want, content)
		}
	}
	for _, re := range []string{
		`(?m)^\s+author\s+Users @relation\(fields: \[author_id\], references: \[id\], name: "posts_author_fkey"\)$`,
		`(?m)^\s+editor\s+Users\? @relation\(fields: \[editorId\], references: \[id\], name: "posts_editor_fkey"\)$`,
		`(?m)^\s+parent\s+Posts\? @relation\(fields: \[parent_id\], references: \[id\], name: "posts_parent_fkey"\)$`,
		`(?m)^\s+posts_parent\s+Posts\[\]$`,
		`(?m)^\s+posts_author\s+Posts\[\]$`,
		`(?m)^\s+posts_editor\s+Posts\[\]$`,
	} {
		if !regexp.MustCompile(re).MatchString(content) {
			t.Errorf("generated schema does not match %s\n%s", re, content)
		}
	}

	compiledUsers := findModelByTable(result.Schema, "users")
	if compiledUsers == nil {
		t.Fatal("users model missing")
	}
	idx := compiledUsers.Indexes[0]
	if idx.Columns[0].Sort != "DESC" || idx.Columns[0].Nulls != "LAST" || idx.Columns[1].OpClass != "varchar_pattern_ops" || idx.Columns[1].Collation != "C" {
		t.Errorf("compiled index options = %#v", idx.Columns)
	}
	if f := findFieldByDBName(compiledUsers, "email_lower"); f == nil || f.Default == nil || f.Default.FuncName != "dbgenerated" || f.Default.FuncArgs[0] != "lower((email)::text)" {
		t.Errorf("compiled generated column = %#v", f)
	}
	if f := findFieldByDBName(compiledUsers, "email"); f == nil || f.NativeType == nil || f.NativeType.Name != "VarChar" {
		t.Errorf("compiled email = %#v", f)
	}
}

func TestGenerateSchemaFromIRSingleForeignKeyKeepsTableName(t *testing.T) {
	schema := &ir.Schema{Models: []*ir.Model{
		{Name: "users", Fields: []*ir.Field{{Name: "id", ScalarType: "Int", IsID: true}}, PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}}},
		{Name: "profiles", Fields: []*ir.Field{
			{Name: "id", ScalarType: "Int", IsID: true},
			{Name: "user_id", ScalarType: "Int", IsUnique: true},
		}, PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}}, Relations: []*ir.Relation{
			{Fields: []string{"user_id"}, ToModel: "users", References: []string{"id"}},
		}},
	}}
	content := GenerateSchemaWithEnums("postgresql", tablesFromIR(schema), nil)
	if result := compiler.CompileFile("schema.gcorm", []byte(content)); result.HasErrors() {
		t.Fatalf("compile: %v\n%s", result.Errors, content)
	}
	for _, re := range []string{
		`(?m)^\s+users\s+Users @relation\(fields: \[user_id\], references: \[id\]\)$`,
		// user_id is unique, so the back-relation is one-to-one.
		`(?m)^\s+profiles\s+Profiles\?$`,
	} {
		if !regexp.MustCompile(re).MatchString(content) {
			t.Errorf("generated schema does not match %s\n%s", re, content)
		}
	}
}

func TestSchemaDefaultExpressionLiteralTyping(t *testing.T) {
	tests := []struct {
		field *ir.Field
		want  string
	}{
		{&ir.Field{ScalarType: "Int", Default: &ir.DefaultValue{Value: "7", IsNumber: true}}, "7"},
		{&ir.Field{ScalarType: "Int", Default: &ir.DefaultValue{Value: "abc", IsString: true}}, `dbgenerated("'abc'")`},
		{&ir.Field{ScalarType: "Boolean", Default: &ir.DefaultValue{Value: "true", IsBool: true}}, "true"},
		{&ir.Field{ScalarType: "String", Default: &ir.DefaultValue{Value: "true", IsBool: true}}, `"true"`},
		{&ir.Field{ScalarType: "String", Default: &ir.DefaultValue{Value: "say \"hi\"\n", IsString: true}}, `"say \"hi\"\n"`},
		{&ir.Field{ScalarType: "String", Default: &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}}, ""},
		{&ir.Field{ScalarType: "BigInt", Default: &ir.DefaultValue{IsFunction: true, FuncName: "autoincrement"}}, "autoincrement()"},
		{&ir.Field{ScalarType: "String", Default: &ir.DefaultValue{IsFunction: true, FuncName: "cuid"}}, "cuid()"},
	}
	for _, tc := range tests {
		got, _ := schemaDefaultExpression("postgresql", tc.field, nil)
		if got != tc.want {
			t.Errorf("schemaDefaultExpression(%s %#v) = %q, want %q", tc.field.ScalarType, *tc.field.Default, got, tc.want)
		}
	}
}

func TestSanitizeEnumValueName(t *testing.T) {
	tests := map[string]string{
		"ADMIN":       "ADMIN",
		"admin":       "admin",
		"in progress": "in_progress",
		"1st":         "V_1st",
		"a-b--c":      "a_b_c",
		"!!":          "VALUE",
		"false":       "false_value",
		"Ünïcode":     "Ünïcode",
	}
	for in, want := range tests {
		if got := sanitizeEnumValueName(in); got != want {
			t.Errorf("sanitizeEnumValueName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGenerateSchemaAvoidsScalarTypeNames(t *testing.T) {
	tables := []TableInfo{{Name: "json", Columns: []ColumnInfo{{Name: "id", DataType: "INT", IsPrimaryKey: true}}}}
	content := GenerateSchemaWithEnums("postgresql", tables, []EnumInfo{{Name: "string", Values: []string{"A"}}})
	if result := compiler.CompileFile("schema.gcorm", []byte(content)); result.HasErrors() {
		t.Fatalf("compile: %v\n%s", result.Errors, content)
	}
	if !strings.Contains(content, "model Json2 {") || !strings.Contains(content, "enum String2 {") {
		t.Fatalf("scalar type names were not avoided:\n%s", content)
	}
}
