package dbpush

import (
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
)

func TestPostgresNativeTypeDecimalDefault(t *testing.T) {
	if got := postgresNativeType("numeric(65,30)", "Decimal"); got != nil {
		t.Fatalf("numeric(65,30) = %#v, want nil (default Decimal DDL)", got)
	}
	if got := postgresNativeType("numeric(10,2)", "Decimal"); got == nil || got.Name != "Decimal" {
		t.Fatalf("numeric(10,2) = %#v", got)
	}
	if got := postgresNativeType("character(10)", "String"); got == nil || got.Name != "Char" || strings.Join(got.Args, ",") != "10" {
		t.Fatalf("character(10) = %#v", got)
	}
}

func TestPostgresNativeTypeAliases(t *testing.T) {
	tests := []struct {
		formatted string
		native    *ir.NativeType
		want      bool
	}{
		{"integer", &ir.NativeType{Name: "Int"}, true},
		{"integer", &ir.NativeType{Name: "Integer"}, true},
		{"smallint", &ir.NativeType{Name: "SmallInt"}, true},
		{"bigint", &ir.NativeType{Name: "BigInt"}, true},
		{"time without time zone", &ir.NativeType{Name: "Time"}, true},
		{"time(3) without time zone", &ir.NativeType{Name: "Time", Args: []string{"3"}}, true},
		{"time with time zone", &ir.NativeType{Name: "Timetz"}, true},
		{"character(10)", &ir.NativeType{Name: "Char", Args: []string{"10"}}, true},
		{"character(1)", &ir.NativeType{Name: "Char"}, true},
		{"character varying", &ir.NativeType{Name: "VarChar"}, true},
		{"bit(1)", &ir.NativeType{Name: "Bit"}, true},
		{"bit varying(8)", &ir.NativeType{Name: "VarBit", Args: []string{"8"}}, true},
		{"double precision", &ir.NativeType{Name: "DoublePrecision"}, true},
		{"uuid", &ir.NativeType{Name: "Uuid"}, true},
		{"jsonb", &ir.NativeType{Name: "JsonB"}, true},
		{"date", &ir.NativeType{Name: "Date"}, true},
		{"character(10)", &ir.NativeType{Name: "Char", Args: []string{"12"}}, false},
		{"integer", &ir.NativeType{Name: "BigInt"}, false},
	}
	for _, tt := range tests {
		if got := postgresNativeTypeMatches(tt.formatted, tt.native, false); got != tt.want {
			t.Errorf("postgresNativeTypeMatches(%q, %s) = %v, want %v", tt.formatted, tt.native.Name, got, tt.want)
		}
	}
}

func TestApplyPostgresColumnTypeAdoptsDeclaredScalar(t *testing.T) {
	field := &ir.Field{Type: ir.FieldKindScalar}
	target := &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String", NativeType: &ir.NativeType{Name: "Uuid"}}
	applyPostgresColumnType(field, "uuid", "uuid", "uuid", target, nil)
	if field.ScalarType != "String" || field.NativeType == nil || field.NativeType.Name != "Uuid" {
		t.Fatalf("String @db.Uuid column = %#v", field)
	}
	// Without the declaration a uuid column is a UUID scalar.
	field = &ir.Field{Type: ir.FieldKindScalar}
	applyPostgresColumnType(field, "uuid", "uuid", "uuid", &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String"}, nil)
	if field.ScalarType != "UUID" || field.NativeType != nil {
		t.Fatalf("uuid column = %#v", field)
	}
	if scalar, _ := postgresColumnType("ARRAY", "_timetz"); scalar != "DateTime" {
		t.Fatalf("timetz[] scalar = %q", scalar)
	}
}

func TestMapPostgresEnumDefault(t *testing.T) {
	enums := []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "ADMIN", DBName: "admin"}, {Name: "USER"}}}}
	field := &ir.Field{Type: ir.FieldKindEnum, EnumType: "Role", Default: &ir.DefaultValue{Value: "admin", IsString: true}}
	mapPostgresEnumDefault(field, enums)
	if field.Default.Value != "ADMIN" || field.Default.IsString {
		t.Fatalf("default = %#v", field.Default)
	}
	field = &ir.Field{Type: ir.FieldKindEnum, EnumType: "Role", IsList: true, Default: &ir.DefaultValue{IsArray: true, ArrayValue: []string{"USER", "admin", "gone"}}}
	mapPostgresEnumDefault(field, enums)
	if strings.Join(field.Default.ArrayValue, ",") != "USER,ADMIN,gone" {
		t.Fatalf("array default = %#v", field.Default)
	}
}

func TestAdoptDbGeneratedDefault(t *testing.T) {
	target := &ir.Field{Default: &ir.DefaultValue{IsFunction: true, FuncName: "dbgenerated", FuncArgs: []string{"md5(random()::text)"}}}
	field := &ir.Field{Default: &ir.DefaultValue{Value: "md5((random())::text)"}}
	adoptDbGeneratedDefault(field, target)
	if field.Default.FuncName != "dbgenerated" || field.Default.FuncArgs[0] != "md5(random()::text)" {
		t.Fatalf("default = %#v", field.Default)
	}
	// A missing column default still differs from the declaration.
	field = &ir.Field{}
	adoptDbGeneratedDefault(field, target)
	if field.Default != nil {
		t.Fatalf("default = %#v, want nil", field.Default)
	}
	// Generated columns keep their own expression.
	generated := &ir.Field{IsGenerated: true, Default: &ir.DefaultValue{IsFunction: true, FuncName: "dbgenerated", FuncArgs: []string{"(a * 2)"}}}
	adoptDbGeneratedDefault(generated, target)
	if generated.Default.FuncArgs[0] != "(a * 2)" {
		t.Fatalf("generated default = %#v", generated.Default)
	}
}

func TestMatchTruncatedPostgresNames(t *testing.T) {
	column := strings.Repeat("c", 60)
	target := &ir.Model{Name: "Item", Fields: []*ir.Field{{Name: "value", DBName: column, ScalarType: "String"}},
		Indexes:           []*ir.Index{{Fields: []string{"value"}}},
		UniqueConstraints: []*ir.UniqueConstraint{{Name: "uq_" + strings.Repeat("u", 70), Fields: []string{"value"}}}}
	name, full := migrate.IndexNames(target, target.Indexes[0])
	live := &ir.Model{Name: "Item",
		Indexes:           []*ir.Index{{Name: migrate.PostgresTruncatedIdentifier(full), Fields: []string{column}}, {Name: "other", Fields: []string{column}}},
		UniqueConstraints: []*ir.UniqueConstraint{{Name: migrate.PostgresTruncatedIdentifier(target.UniqueConstraints[0].Name), Fields: []string{column}}}}
	catalog := &postgresCatalog{models: map[string]*ir.Model{"k": live}, targets: map[string]*ir.Model{"k": target}}
	matchTruncatedPostgresNames(catalog)
	if live.Indexes[0].Name != name || live.Indexes[1].Name != "other" || live.UniqueConstraints[0].Name != target.UniqueConstraints[0].Name {
		t.Fatalf("live names = %q, %q, %q; want %q", live.Indexes[0].Name, live.Indexes[1].Name, live.UniqueConstraints[0].Name, name)
	}
}

func TestAdoptDeclaredUniqueIndexes(t *testing.T) {
	target := &ir.Model{Name: "Country", Fields: []*ir.Field{{Name: "id", IsID: true}, {Name: "countryCode", ScalarType: "String"}},
		UniqueConstraints: []*ir.UniqueConstraint{{Fields: []string{"countryCode"}}}}
	plain := &ir.Index{Name: "plain", IsUnique: true, Fields: []string{"country_code"}, Columns: []ir.IndexColumn{{Field: "country_code", Sort: "ASC"}}}
	partial := &ir.Index{Name: "partial", IsUnique: true, Fields: []string{"country_code"}, Where: "id > 0"}
	live := &ir.Model{Name: "Country", Fields: []*ir.Field{{Name: "id"}, {Name: "country_code", DBName: "country_code"}}, Indexes: []*ir.Index{partial, plain}}
	catalog := &postgresCatalog{models: map[string]*ir.Model{"k": live}, targets: map[string]*ir.Model{"k": target}}
	adoptDeclaredUniqueIndexes(catalog)
	if len(live.Indexes) != 1 || live.Indexes[0] != partial || len(live.UniqueConstraints) != 1 || live.UniqueConstraints[0].Name != "plain" || !live.Fields[1].IsUnique {
		t.Fatalf("live = indexes %#v, unique %#v", live.Indexes, live.UniqueConstraints)
	}
	if isPlainPostgresIndex(&ir.Index{Columns: []ir.IndexColumn{{Field: "a", Sort: "DESC"}}}) {
		t.Fatal("descending index is not plain")
	}
}

func TestUpStatementsCountsCommitSteps(t *testing.T) {
	old := &ir.Schema{Enums: []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}}}},
		Models: []*ir.Model{{Name: "Item", Fields: []*ir.Field{{Name: "id", ScalarType: "Int", IsID: true}}}}}
	next := &ir.Schema{Enums: []*ir.Enum{{Name: "Role", Values: []*ir.EnumValue{{Name: "USER"}, {Name: "ADMIN"}}}},
		Models: []*ir.Model{{Name: "Item", Fields: []*ir.Field{{Name: "id", ScalarType: "Int", IsID: true},
			{Name: "role", Type: ir.FieldKindEnum, EnumType: "Role", Default: &ir.DefaultValue{Value: "ADMIN"}}}}}}
	stmts, commitCount, unsupported := upStatements(migrate.DDLGenerator{Dialect: "postgresql", Schema: next}, migrate.Diff(old, next))
	if len(unsupported) != 0 || commitCount != 1 || len(stmts) != 2 ||
		stmts[0] != `ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'ADMIN' AFTER 'USER'` {
		t.Fatalf("statements = %q, commit = %d, unsupported = %q", stmts, commitCount, unsupported)
	}
}

func TestSplitPostgresSearchPath(t *testing.T) {
	got := splitPostgresSearchPath(` "$user", "My ""Schema""" , public,,`)
	if strings.Join(got, "|") != `$user|My "Schema"|public` {
		t.Fatalf("splitPostgresSearchPath() = %q", got)
	}
}
