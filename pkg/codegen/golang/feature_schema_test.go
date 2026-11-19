package golang

import "github.com/arsfy/gcorm/pkg/schema/ir"

// featureSchema returns a schema exercising the PostgreSQL-sensitive client
// features: @@schema, enum @map, database defaults, cuid(), scalar lists,
// Decimal/DateTime, a generated column, a compound unique key and a model
// without a primary key.
func featureSchema(provider string) *ir.Schema {
	fn := func(name string, args ...string) *ir.DefaultValue {
		return &ir.DefaultValue{IsFunction: true, FuncName: name, FuncArgs: args}
	}
	return &ir.Schema{
		Datasource: &ir.Datasource{Provider: provider},
		Generators: []*ir.Generator{{Name: "client", Provider: "gco-go", Output: "./gen", Package: "db"}},
		Models: []*ir.Model{
			{
				Name:   "Account",
				DBName: "accounts",
				Schema: "audit",
				Fields: []*ir.Field{
					{Name: "id", Type: ir.FieldKindScalar, ScalarType: "Int", IsID: true, Default: fn("autoincrement")},
					{Name: "code", Type: ir.FieldKindScalar, ScalarType: "String", IsUnique: true, Default: fn("cuid")},
					{Name: "uid", Type: ir.FieldKindScalar, ScalarType: "String", Default: fn("uuid")},
					{Name: "tenant", Type: ir.FieldKindScalar, ScalarType: "String"},
					{Name: "email", Type: ir.FieldKindScalar, ScalarType: "String"},
					{Name: "role", Type: ir.FieldKindEnum, EnumType: "Role", Default: &ir.DefaultValue{Value: "ADMIN"}},
					{Name: "roles", Type: ir.FieldKindEnum, EnumType: "Role", IsList: true},
					{Name: "tags", Type: ir.FieldKindScalar, ScalarType: "String", IsList: true},
					{Name: "scores", Type: ir.FieldKindScalar, ScalarType: "Int", IsList: true},
					{Name: "balance", Type: ir.FieldKindScalar, ScalarType: "Decimal", Default: &ir.DefaultValue{Value: "0", IsNumber: true}},
					{Name: "createdAt", Type: ir.FieldKindScalar, ScalarType: "DateTime", Default: fn("now")},
					{Name: "nickname", Type: ir.FieldKindScalar, ScalarType: "String", IsOptional: true, Default: &ir.DefaultValue{Value: "anon", IsString: true}},
					{Name: "note", Type: ir.FieldKindScalar, ScalarType: "String", IsOptional: true},
					{Name: "slug", Type: ir.FieldKindScalar, ScalarType: "String", IsGenerated: true, Default: fn("dbgenerated", `"lower(email)"`)},
				},
				PrimaryKey:        &ir.PrimaryKey{Fields: []string{"id"}},
				UniqueConstraints: []*ir.UniqueConstraint{{Fields: []string{"tenant", "email"}}},
			},
			{
				Name:   "Event",
				DBName: "events",
				Fields: []*ir.Field{
					{Name: "name", Type: ir.FieldKindScalar, ScalarType: "String"},
					{Name: "n", Type: ir.FieldKindScalar, ScalarType: "Int"},
				},
			},
			{
				// Every writable column has a database default.
				Name:   "Counter",
				DBName: "counters",
				Fields: []*ir.Field{
					{Name: "id", Type: ir.FieldKindScalar, ScalarType: "Int", IsID: true, Default: fn("autoincrement")},
					{Name: "n", Type: ir.FieldKindScalar, ScalarType: "Int", Default: &ir.DefaultValue{Value: "0", IsNumber: true}},
				},
				PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
			},
		},
		Enums: []*ir.Enum{
			{Name: "Role", Values: []*ir.EnumValue{{Name: "ADMIN", DBName: "admin"}, {Name: "USER"}}},
		},
	}
}
