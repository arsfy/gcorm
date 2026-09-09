package resolve

import (
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ast"
	"github.com/arsfy/gcorm/pkg/schema/ir"
)

// helper to wrap a Document into a DocumentSet.
func docSet(doc *ast.Document) *ast.DocumentSet {
	return &ast.DocumentSet{Documents: []*ast.Document{doc}}
}

// ---------------------------------------------------------------------------
// 1. Resolve datasource
// ---------------------------------------------------------------------------

func TestResolveDatasource(t *testing.T) {
	ds := docSet(&ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: ast.StringLiteral{Value: "postgresql"}},
				{Key: "url", Value: ast.FunctionCall{
					Name: "env",
					Args: []ast.Expression{ast.StringLiteral{Value: "DATABASE_URL"}},
				}},
				{Key: "schema", Value: ast.StringLiteral{Value: "public"}},
			},
		}},
		Models: []ast.ModelDecl{{Name: "Dummy", Fields: []ast.FieldDecl{
			{Name: "id", Type: ast.FieldType{Name: "Int"}},
		}}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if schema.Datasource == nil {
		t.Fatal("expected datasource")
	}
	d := schema.Datasource
	if d.Provider != "postgresql" {
		t.Errorf("provider = %q, want %q", d.Provider, "postgresql")
	}
	if !d.URLIsEnv {
		t.Error("expected URLIsEnv = true")
	}
	if d.EnvVar != "DATABASE_URL" {
		t.Errorf("envVar = %q, want %q", d.EnvVar, "DATABASE_URL")
	}
	if d.Schema != "public" {
		t.Errorf("schema = %q, want %q", d.Schema, "public")
	}
}

// ---------------------------------------------------------------------------
// 2. Resolve generator
// ---------------------------------------------------------------------------

func TestResolveGenerator(t *testing.T) {
	ds := docSet(&ast.Document{
		Generators: []ast.GeneratorDecl{{
			Name: "client",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: ast.StringLiteral{Value: "gco-go-client"}},
				{Key: "output", Value: ast.StringLiteral{Value: "./generated"}},
				{Key: "package", Value: ast.StringLiteral{Value: "db"}},
				{Key: "emitRuntime", Value: ast.BooleanLiteral{Value: true}},
			},
		}},
		Models: []ast.ModelDecl{{Name: "Dummy", Fields: []ast.FieldDecl{
			{Name: "id", Type: ast.FieldType{Name: "Int"}},
		}}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(schema.Generators) != 1 {
		t.Fatalf("expected 1 generator, got %d", len(schema.Generators))
	}
	g := schema.Generators[0]
	if g.Provider != "gco-go-client" {
		t.Errorf("provider = %q", g.Provider)
	}
	if g.Output != "./generated" {
		t.Errorf("output = %q", g.Output)
	}
	if g.Package != "db" {
		t.Errorf("package = %q", g.Package)
	}
	if !g.EmitRuntime {
		t.Error("expected emitRuntime = true")
	}
}

// ---------------------------------------------------------------------------
// 3. Resolve simple model
// ---------------------------------------------------------------------------

func TestResolveSimpleModel(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "score", Type: ast.FieldType{Name: "SmallInt"}},
				{Name: "email", Type: ast.FieldType{Name: "String"}},
				{Name: "active", Type: ast.FieldType{Name: "Boolean", IsOptional: true}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(schema.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(schema.Models))
	}
	m := schema.Models[0]
	if m.Name != "User" {
		t.Errorf("model name = %q", m.Name)
	}
	if len(m.Fields) != 4 {
		t.Fatalf("expected 4 fields, got %d", len(m.Fields))
	}
	// id field
	f := m.Fields[0]
	if f.Type != ir.FieldKindScalar || f.ScalarType != "Int" {
		t.Errorf("field[0] type = %v, scalar = %q", f.Type, f.ScalarType)
	}
	// score field
	f = m.Fields[1]
	if f.Type != ir.FieldKindScalar || f.ScalarType != "SmallInt" {
		t.Errorf("field[1] type = %v, scalar = %q", f.Type, f.ScalarType)
	}
	// email field
	f = m.Fields[2]
	if f.Type != ir.FieldKindScalar || f.ScalarType != "String" {
		t.Errorf("field[2] type = %v, scalar = %q", f.Type, f.ScalarType)
	}
	// active field
	f = m.Fields[3]
	if !f.IsOptional {
		t.Error("expected active to be optional")
	}
}

// ---------------------------------------------------------------------------
// 4. Resolve model with @id and @default
// ---------------------------------------------------------------------------

func TestResolveIDAndDefault(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{
					Name: "id",
					Type: ast.FieldType{Name: "Int"},
					Attributes: []ast.Attribute{
						{Name: "id"},
						{Name: "default", Args: []ast.AttributeArg{
							{Value: ast.FunctionCall{Name: "autoincrement"}},
						}},
					},
				},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	f := schema.Models[0].Fields[0]
	if !f.IsID {
		t.Error("expected IsID = true")
	}
	if f.Default == nil {
		t.Fatal("expected Default")
	}
	if !f.Default.IsFunction || f.Default.FuncName != "autoincrement" {
		t.Errorf("default = %+v", f.Default)
	}
	pk := schema.Models[0].PrimaryKey
	if pk == nil {
		t.Fatal("expected PrimaryKey")
	}
	if pk.IsComposite {
		t.Error("expected single-field PK")
	}
	if len(pk.Fields) != 1 || pk.Fields[0] != "id" {
		t.Errorf("pk fields = %v", pk.Fields)
	}
}

// ---------------------------------------------------------------------------
// 5. Resolve model with @map
// ---------------------------------------------------------------------------

func TestResolveFieldMap(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{
					Name: "firstName",
					Type: ast.FieldType{Name: "String"},
					Attributes: []ast.Attribute{
						{Name: "map", Args: []ast.AttributeArg{
							{Value: ast.StringLiteral{Value: "first_name"}},
						}},
					},
				},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	f := schema.Models[0].Fields[0]
	if f.DBName != "first_name" {
		t.Errorf("DBName = %q, want %q", f.DBName, "first_name")
	}
}

func TestResolveListDefault(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "KeySet",
			Fields: []ast.FieldDecl{{
				Name: "sshkeys",
				Type: ast.FieldType{Name: "BigInt", IsList: true},
				Attributes: []ast.Attribute{{
					Name: "default",
					Args: []ast.AttributeArg{{
						Value: ast.ArrayLiteral{},
					}},
				}},
			}},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	field := schema.Models[0].Fields[0]
	if field.Default == nil || !field.Default.IsArray {
		t.Fatalf("default = %#v, want array default", field.Default)
	}
	if len(field.Default.ArrayValue) != 0 {
		t.Fatalf("array default values = %v, want empty", field.Default.ArrayValue)
	}
}

// ---------------------------------------------------------------------------
// 6. Resolve model with @relation
// ---------------------------------------------------------------------------

func TestResolveRelation(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{
			{
				Name: "Post",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					{Name: "authorId", Type: ast.FieldType{Name: "Int"}},
					{
						Name: "author",
						Type: ast.FieldType{Name: "User"},
						Attributes: []ast.Attribute{{
							Name: "relation",
							Args: []ast.AttributeArg{
								{Name: "fields", Value: ast.ArrayLiteral{Elements: []ast.Expression{ast.Identifier{Name: "authorId"}}}},
								{Name: "references", Value: ast.ArrayLiteral{Elements: []ast.Expression{ast.Identifier{Name: "id"}}}},
							},
						}},
					},
				},
			},
			{
				Name: "User",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					{Name: "posts", Type: ast.FieldType{Name: "Post", IsList: true}},
				},
			},
		},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Post model
	post := schema.Models[0]
	if len(post.Relations) != 1 {
		t.Fatalf("expected 1 relation on Post, got %d", len(post.Relations))
	}
	rel := post.Relations[0]
	if rel.FromModel != "Post" || rel.ToModel != "User" {
		t.Errorf("relation from=%q to=%q", rel.FromModel, rel.ToModel)
	}
	if len(rel.Fields) != 1 || rel.Fields[0] != "authorId" {
		t.Errorf("relation fields = %v", rel.Fields)
	}
	if len(rel.References) != 1 || rel.References[0] != "id" {
		t.Errorf("relation references = %v", rel.References)
	}
	if rel.Type != ir.RelationManyToOne {
		t.Errorf("expected ManyToOne, got %v", rel.Type)
	}

	// User model - posts is OneToMany
	user := schema.Models[1]
	if len(user.Relations) != 1 {
		t.Fatalf("expected 1 relation on User, got %d", len(user.Relations))
	}
	if user.Relations[0].Type != ir.RelationOneToMany {
		t.Errorf("expected OneToMany, got %v", user.Relations[0].Type)
	}
}

// ---------------------------------------------------------------------------
// 7. Resolve enum
// ---------------------------------------------------------------------------

func TestResolveEnum(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{Name: "Dummy", Fields: []ast.FieldDecl{
			{Name: "id", Type: ast.FieldType{Name: "Int"}},
		}}},
		Enums: []ast.EnumDecl{{
			Name: "Role",
			Values: []ast.EnumValue{
				{Name: "ADMIN", Attributes: []ast.Attribute{
					{Name: "map", Args: []ast.AttributeArg{
						{Value: ast.StringLiteral{Value: "admin"}},
					}},
				}},
				{Name: "USER"},
			},
			Attributes: []ast.Attribute{
				{Name: "map", IsModelLevel: true, Args: []ast.AttributeArg{
					{Value: ast.StringLiteral{Value: "roles"}},
				}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(schema.Enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(schema.Enums))
	}
	e := schema.Enums[0]
	if e.Name != "Role" {
		t.Errorf("enum name = %q", e.Name)
	}
	if e.DBName != "roles" {
		t.Errorf("enum DBName = %q, want %q", e.DBName, "roles")
	}
	if len(e.Values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(e.Values))
	}
	if e.Values[0].DBName != "admin" {
		t.Errorf("value[0] DBName = %q, want %q", e.Values[0].DBName, "admin")
	}
	if e.Values[1].DBName != "" {
		t.Errorf("value[1] DBName = %q, want empty", e.Values[1].DBName)
	}
}

// ---------------------------------------------------------------------------
// 8. Resolve @@index
// ---------------------------------------------------------------------------

func TestResolveIndex(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "email", Type: ast.FieldType{Name: "String"}},
			},
			Attributes: []ast.Attribute{
				{Name: "index", IsModelLevel: true, Args: []ast.AttributeArg{
					{Name: "fields", Value: ast.ArrayLiteral{Elements: []ast.Expression{
						ast.Identifier{Name: "email"},
					}}},
					{Name: "name", Value: ast.StringLiteral{Value: "idx_email"}},
				}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	m := schema.Models[0]
	if len(m.Indexes) != 1 {
		t.Fatalf("expected 1 index, got %d", len(m.Indexes))
	}
	idx := m.Indexes[0]
	if idx.Name != "idx_email" {
		t.Errorf("index name = %q", idx.Name)
	}
	if len(idx.Fields) != 1 || idx.Fields[0] != "email" {
		t.Errorf("index fields = %v", idx.Fields)
	}
	if idx.IsUnique {
		t.Error("expected non-unique index")
	}
}

func TestResolveIndexPredicateAndColumnOptions(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "Announcement",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "status", Type: ast.FieldType{Name: "Int"}},
				{Name: "publishedAt", Type: ast.FieldType{Name: "DateTime", IsOptional: true}},
			},
			Attributes: []ast.Attribute{
				{Name: "index", IsModelLevel: true, Args: []ast.AttributeArg{
					{Value: ast.ArrayLiteral{Elements: []ast.Expression{
						ast.Identifier{Name: "status"},
						ast.Identifier{Name: "publishedAt"},
					}}},
					{Name: "name", Value: ast.StringLiteral{Value: "IDX_ANNOUNCEMENTS_USER"}},
					{Name: "where", Value: ast.StringLiteral{Value: "status = 1 AND published_at IS NOT NULL"}},
					{Name: "sort", Value: ast.ArrayLiteral{Elements: []ast.Expression{
						ast.Identifier{Name: "Desc"},
						ast.Identifier{Name: "Asc"},
					}}},
					{Name: "nulls", Value: ast.ArrayLiteral{Elements: []ast.Expression{
						ast.Identifier{Name: "Last"},
						ast.Identifier{Name: "Last"},
					}}},
					{Name: "opclass", Value: ast.ArrayLiteral{Elements: []ast.Expression{
						ast.StringLiteral{Value: "int8_ops"},
						ast.StringLiteral{Value: "timestamptz_ops"},
					}}},
					{Name: "collate", Value: ast.ArrayLiteral{Elements: []ast.Expression{
						ast.StringLiteral{Value: "pg_catalog.default"},
						ast.StringLiteral{Value: "pg_catalog.default"},
					}}},
				}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	idx := schema.Models[0].Indexes[0]
	if idx.Where != "status = 1 AND published_at IS NOT NULL" {
		t.Fatalf("where = %q", idx.Where)
	}
	if len(idx.Columns) != 2 {
		t.Fatalf("columns = %d, want 2", len(idx.Columns))
	}
	if idx.Columns[0].Field != "status" || idx.Columns[0].Sort != "DESC" || idx.Columns[0].Nulls != "LAST" || idx.Columns[0].OpClass != "int8_ops" {
		t.Fatalf("first column = %+v", idx.Columns[0])
	}
	if idx.Columns[1].Field != "publishedAt" || idx.Columns[1].Sort != "ASC" || idx.Columns[1].Nulls != "LAST" || idx.Columns[1].OpClass != "timestamptz_ops" {
		t.Fatalf("second column = %+v", idx.Columns[1])
	}
	if idx.Columns[0].Collation != "pg_catalog.default" || idx.Columns[1].Collation != "pg_catalog.default" {
		t.Fatalf("collations = %+v", idx.Columns)
	}
}

func TestResolveExpressionIndexes(t *testing.T) {
	ds := docSet(&ast.Document{Models: []ast.ModelDecl{{
		Name:   "User",
		Fields: []ast.FieldDecl{{Name: "email", Type: ast.FieldType{Name: "String"}}},
		Attributes: []ast.Attribute{
			{Name: "index", IsModelLevel: true, Args: []ast.AttributeArg{
				{Value: ast.ArrayLiteral{Elements: []ast.Expression{ast.Identifier{Name: "email"}}}},
				{Name: "expression", Value: ast.StringLiteral{Value: "lower(email)"}},
			}},
			{Name: "unique", IsModelLevel: true, Args: []ast.AttributeArg{
				{Value: ast.ArrayLiteral{Elements: []ast.Expression{ast.Identifier{Name: "email"}}}},
				{Name: "name", Value: ast.StringLiteral{Value: "uq_users_email_ci"}},
				{Name: "expression", Value: ast.StringLiteral{Value: "lower(email)"}},
			}},
		},
	}}})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	model := schema.Models[0]
	if len(model.Indexes) != 2 || len(model.UniqueConstraints) != 0 {
		t.Fatalf("indexes = %d, unique constraints = %d", len(model.Indexes), len(model.UniqueConstraints))
	}
	if got := model.Indexes[0].Columns[0].Expression; got != "lower(email)" {
		t.Fatalf("index expression = %q", got)
	}
	if !model.Indexes[1].IsUnique || model.Indexes[1].Name != "uq_users_email_ci" {
		t.Fatalf("expression unique index = %+v", model.Indexes[1])
	}
}

// ---------------------------------------------------------------------------
// 9. Resolve @@id composite
// ---------------------------------------------------------------------------

func TestResolveCompositeID(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "PostTag",
			Fields: []ast.FieldDecl{
				{Name: "postId", Type: ast.FieldType{Name: "Int"}},
				{Name: "tagId", Type: ast.FieldType{Name: "Int"}},
			},
			Attributes: []ast.Attribute{
				{Name: "id", IsModelLevel: true, Args: []ast.AttributeArg{
					{Value: ast.ArrayLiteral{Elements: []ast.Expression{
						ast.Identifier{Name: "postId"},
						ast.Identifier{Name: "tagId"},
					}}},
				}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	pk := schema.Models[0].PrimaryKey
	if pk == nil {
		t.Fatal("expected PrimaryKey")
	}
	if !pk.IsComposite {
		t.Error("expected composite PK")
	}
	if len(pk.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(pk.Fields))
	}
	if pk.Fields[0] != "postId" || pk.Fields[1] != "tagId" {
		t.Errorf("pk fields = %v", pk.Fields)
	}
}

// ---------------------------------------------------------------------------
// 10. Resolve field type to enum
// ---------------------------------------------------------------------------

func TestResolveFieldTypeEnum(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "role", Type: ast.FieldType{Name: "Role"}},
			},
		}},
		Enums: []ast.EnumDecl{{
			Name:   "Role",
			Values: []ast.EnumValue{{Name: "ADMIN"}, {Name: "USER"}},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	f := schema.Models[0].Fields[1]
	if f.Type != ir.FieldKindEnum {
		t.Errorf("expected FieldKindEnum, got %v", f.Type)
	}
	if f.EnumType != "Role" {
		t.Errorf("enum type = %q", f.EnumType)
	}
}

// ---------------------------------------------------------------------------
// 11. Resolve field type to model
// ---------------------------------------------------------------------------

func TestResolveFieldTypeModel(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{
			{
				Name: "Post",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					{Name: "author", Type: ast.FieldType{Name: "User"}},
				},
			},
			{
				Name: "User",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
				},
			},
		},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	f := schema.Models[0].Fields[1]
	if f.Type != ir.FieldKindRelation {
		t.Errorf("expected FieldKindRelation, got %v", f.Type)
	}
	if f.ModelType != "User" {
		t.Errorf("model type = %q", f.ModelType)
	}
}

// ---------------------------------------------------------------------------
// 12. Unknown field type
// ---------------------------------------------------------------------------

func TestResolveUnknownType(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "Foo",
			Fields: []ast.FieldDecl{
				{Name: "x", Type: ast.FieldType{Name: "NoSuchType"}},
			},
		}},
	})

	_, errs := Resolve(ds)
	if len(errs) == 0 {
		t.Fatal("expected error for unknown type")
	}
	got := errs[0].Error()
	if got == "" {
		t.Fatal("error message is empty")
	}
}

// ---------------------------------------------------------------------------
// 13. Model.TableName
// ---------------------------------------------------------------------------

func TestModelTableName(t *testing.T) {
	m1 := &ir.Model{Name: "User"}
	if m1.TableName() != "User" {
		t.Errorf("TableName() = %q, want %q", m1.TableName(), "User")
	}

	m2 := &ir.Model{Name: "User", DBName: "users"}
	if m2.TableName() != "users" {
		t.Errorf("TableName() = %q, want %q", m2.TableName(), "users")
	}
}

// ---------------------------------------------------------------------------
// 14. Model.ScalarFields
// ---------------------------------------------------------------------------

func TestModelScalarFields(t *testing.T) {
	m := &ir.Model{
		Name: "Post",
		Fields: []*ir.Field{
			{Name: "id", Type: ir.FieldKindScalar},
			{Name: "title", Type: ir.FieldKindScalar},
			{Name: "author", Type: ir.FieldKindRelation},
			{Name: "status", Type: ir.FieldKindEnum},
		},
	}
	scalars := m.ScalarFields()
	if len(scalars) != 3 {
		t.Fatalf("expected 3 scalar fields (scalar+enum), got %d", len(scalars))
	}
	for _, f := range scalars {
		if f.Type == ir.FieldKindRelation {
			t.Errorf("unexpected relation field %q in scalar fields", f.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// 15. ResolveError formatting
// ---------------------------------------------------------------------------

func TestResolveErrorString(t *testing.T) {
	withPos := &ResolveError{Message: "boom", Pos: ast.Position{File: "schema.gcorm", Line: 3, Column: 7}}
	if got, want := withPos.Error(), "schema.gcorm:3:7: boom"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	noPos := &ResolveError{Message: "boom"}
	if got := noPos.Error(); got != "boom" {
		t.Errorf("Error() = %q, want %q", got, "boom")
	}
}

// ---------------------------------------------------------------------------
// 16. Datasource: plain URL, schemas list, empty env()
// ---------------------------------------------------------------------------

func TestResolveDatasourcePlainURLAndSchemas(t *testing.T) {
	ds := docSet(&ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: ast.StringLiteral{Value: "postgresql"}},
				{Key: "url", Value: ast.StringLiteral{Value: "postgresql://localhost/db"}},
				{Key: "schemas", Value: ast.ArrayLiteral{Elements: []ast.Expression{
					ast.StringLiteral{Value: "public"},
					ast.StringLiteral{Value: "billing"},
				}}},
				{Key: "unknownKey", Value: ast.StringLiteral{Value: "ignored"}},
			},
		}},
		Models: []ast.ModelDecl{{Name: "Dummy", Fields: []ast.FieldDecl{
			{Name: "id", Type: ast.FieldType{Name: "Int"}},
		}}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	d := schema.Datasource
	if d.URLIsEnv {
		t.Error("expected URLIsEnv = false for plain url")
	}
	if d.URL != "postgresql://localhost/db" {
		t.Errorf("url = %q", d.URL)
	}
	if len(d.Schemas) != 2 || d.Schemas[0] != "public" || d.Schemas[1] != "billing" {
		t.Errorf("schemas = %v", d.Schemas)
	}
}

func TestResolveDatasourceEnvWithoutArgs(t *testing.T) {
	ds := docSet(&ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "url", Value: ast.FunctionCall{Name: "env"}},
			},
		}},
		Models: []ast.ModelDecl{{Name: "Dummy", Fields: []ast.FieldDecl{
			{Name: "id", Type: ast.FieldType{Name: "Int"}},
		}}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	d := schema.Datasource
	if !d.URLIsEnv {
		t.Error("expected URLIsEnv = true")
	}
	if d.EnvVar != "" || d.URL != "" {
		t.Errorf("envVar = %q, url = %q, want both empty", d.EnvVar, d.URL)
	}
}

// ---------------------------------------------------------------------------
// 17. Model-level @@map and @@schema
// ---------------------------------------------------------------------------

func TestResolveModelMapAndSchema(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
			},
			Attributes: []ast.Attribute{
				{Name: "map", IsModelLevel: true, Args: []ast.AttributeArg{
					{Value: ast.StringLiteral{Value: "users"}},
				}},
				{Name: "schema", IsModelLevel: true, Args: []ast.AttributeArg{
					{Value: ast.StringLiteral{Value: "app"}},
				}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	m := schema.Models[0]
	if m.DBName != "users" {
		t.Errorf("DBName = %q, want %q", m.DBName, "users")
	}
	if m.Schema != "app" {
		t.Errorf("Schema = %q, want %q", m.Schema, "app")
	}
}

// ---------------------------------------------------------------------------
// 18. @@unique without expression -> UniqueConstraint
// ---------------------------------------------------------------------------

func TestResolveUniqueConstraint(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "Membership",
			Fields: []ast.FieldDecl{
				{Name: "userId", Type: ast.FieldType{Name: "Int"}},
				{Name: "teamId", Type: ast.FieldType{Name: "Int"}},
			},
			Attributes: []ast.Attribute{
				{Name: "unique", IsModelLevel: true, Args: []ast.AttributeArg{
					{Name: "fields", Value: ast.ArrayLiteral{Elements: []ast.Expression{
						ast.Identifier{Name: "userId"},
						ast.Identifier{Name: "teamId"},
					}}},
					{Name: "name", Value: ast.StringLiteral{Value: "uq_membership"}},
				}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	m := schema.Models[0]
	if len(m.Indexes) != 0 {
		t.Fatalf("expected 0 indexes, got %d", len(m.Indexes))
	}
	if len(m.UniqueConstraints) != 1 {
		t.Fatalf("expected 1 unique constraint, got %d", len(m.UniqueConstraints))
	}
	uq := m.UniqueConstraints[0]
	if uq.Name != "uq_membership" {
		t.Errorf("unique name = %q", uq.Name)
	}
	if len(uq.Fields) != 2 || uq.Fields[0] != "userId" || uq.Fields[1] != "teamId" {
		t.Errorf("unique fields = %v", uq.Fields)
	}
}

// ---------------------------------------------------------------------------
// 19. Field attributes: @unique, @updatedAt, @db.*, arg-less attributes
// ---------------------------------------------------------------------------

func TestResolveFieldAttributes(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{
					Name: "email",
					Type: ast.FieldType{Name: "String"},
					Attributes: []ast.Attribute{
						{Name: "unique"},
						{Name: "db.VarChar", Args: []ast.AttributeArg{
							{Value: ast.NumberLiteral{Value: "255"}},
						}},
					},
				},
				{
					Name: "updatedAt",
					Type: ast.FieldType{Name: "DateTime"},
					Attributes: []ast.Attribute{
						{Name: "updatedAt"},
					},
				},
				{
					Name: "nick",
					Type: ast.FieldType{Name: "String"},
					Attributes: []ast.Attribute{
						// Arg-less @map and @default must be no-ops.
						{Name: "map"},
						{Name: "default"},
					},
				},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	fields := schema.Models[0].Fields

	email := fields[0]
	if !email.IsUnique {
		t.Error("expected email IsUnique = true")
	}
	if email.NativeType == nil {
		t.Fatal("expected NativeType on email")
	}
	if email.NativeType.Name != "VarChar" {
		t.Errorf("native type name = %q", email.NativeType.Name)
	}
	if len(email.NativeType.Args) != 1 || email.NativeType.Args[0] != "255" {
		t.Errorf("native type args = %v", email.NativeType.Args)
	}

	if !fields[1].IsUpdatedAt {
		t.Error("expected updatedAt IsUpdatedAt = true")
	}

	nick := fields[2]
	if nick.DBName != "" {
		t.Errorf("expected empty DBName for arg-less @map, got %q", nick.DBName)
	}
	if nick.Default != nil {
		t.Errorf("expected nil Default for arg-less @default, got %+v", nick.Default)
	}
}

// ---------------------------------------------------------------------------
// 20. Default value resolution across literal kinds
// ---------------------------------------------------------------------------

func TestResolveDefaultKinds(t *testing.T) {
	cases := []struct {
		name  string
		expr  ast.Expression
		check func(t *testing.T, dv *ir.DefaultValue)
	}{
		{
			name: "string literal",
			expr: ast.StringLiteral{Value: "hello"},
			check: func(t *testing.T, dv *ir.DefaultValue) {
				if !dv.IsString || dv.Value != "hello" {
					t.Errorf("default = %+v", dv)
				}
			},
		},
		{
			name: "number literal",
			expr: ast.NumberLiteral{Value: "42"},
			check: func(t *testing.T, dv *ir.DefaultValue) {
				if !dv.IsNumber || dv.Value != "42" {
					t.Errorf("default = %+v", dv)
				}
			},
		},
		{
			name: "boolean literal",
			expr: ast.BooleanLiteral{Value: true},
			check: func(t *testing.T, dv *ir.DefaultValue) {
				if !dv.IsBool || dv.Value != "true" {
					t.Errorf("default = %+v", dv)
				}
			},
		},
		{
			name: "identifier (enum value)",
			expr: ast.Identifier{Name: "ADMIN"},
			check: func(t *testing.T, dv *ir.DefaultValue) {
				if dv.Value != "ADMIN" || dv.IsFunction || dv.IsString {
					t.Errorf("default = %+v", dv)
				}
			},
		},
		{
			name: "function with args",
			expr: ast.FunctionCall{Name: "dbgenerated", Args: []ast.Expression{
				ast.StringLiteral{Value: "gen_random_uuid()"},
			}},
			check: func(t *testing.T, dv *ir.DefaultValue) {
				if !dv.IsFunction || dv.FuncName != "dbgenerated" {
					t.Errorf("default = %+v", dv)
				}
				if len(dv.FuncArgs) != 1 || dv.FuncArgs[0] != "gen_random_uuid()" {
					t.Errorf("func args = %v", dv.FuncArgs)
				}
			},
		},
		{
			name: "array literal with elements",
			expr: ast.ArrayLiteral{Elements: []ast.Expression{
				ast.StringLiteral{Value: "a"},
				ast.StringLiteral{Value: "b"},
			}},
			check: func(t *testing.T, dv *ir.DefaultValue) {
				if !dv.IsArray {
					t.Errorf("default = %+v", dv)
				}
				if len(dv.ArrayValue) != 2 || dv.ArrayValue[0] != "a" || dv.ArrayValue[1] != "b" {
					t.Errorf("array value = %v", dv.ArrayValue)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := docSet(&ast.Document{
				Models: []ast.ModelDecl{{
					Name: "T",
					Fields: []ast.FieldDecl{{
						Name: "v",
						Type: ast.FieldType{Name: "String"},
						Attributes: []ast.Attribute{{
							Name: "default",
							Args: []ast.AttributeArg{{Value: tc.expr}},
						}},
					}},
				}},
			})
			schema, errs := Resolve(ds)
			if len(errs) > 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			dv := schema.Models[0].Fields[0].Default
			if dv == nil {
				t.Fatal("expected Default")
			}
			tc.check(t, dv)
		})
	}
}

// ---------------------------------------------------------------------------
// 21. Relation variants: OneToOne, name, onDelete/onUpdate
// ---------------------------------------------------------------------------

func TestResolveRelationVariants(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{
			{
				Name: "Profile",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					{Name: "userId", Type: ast.FieldType{Name: "Int"}},
					{
						Name: "user",
						Type: ast.FieldType{Name: "User"},
						Attributes: []ast.Attribute{{
							Name: "relation",
							Args: []ast.AttributeArg{
								{Value: ast.StringLiteral{Value: "UserProfile"}},
								{Name: "fields", Value: ast.ArrayLiteral{Elements: []ast.Expression{ast.Identifier{Name: "userId"}}}},
								{Name: "references", Value: ast.ArrayLiteral{Elements: []ast.Expression{ast.Identifier{Name: "id"}}}},
								{Name: "onDelete", Value: ast.Identifier{Name: "Cascade"}},
								{Name: "onUpdate", Value: ast.Identifier{Name: "NoAction"}},
							},
						}},
					},
				},
			},
			{
				Name: "User",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					// Optional back-reference -> OneToOne; non-relation attrs ignored.
					{
						Name:       "profile",
						Type:       ast.FieldType{Name: "Profile", IsOptional: true},
						Attributes: []ast.Attribute{{Name: "unique"}},
					},
				},
			},
		},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	profile := schema.Models[0]
	rel := profile.Relations[0]
	if rel.Name != "UserProfile" {
		t.Errorf("relation name = %q", rel.Name)
	}
	if rel.OnDelete != "Cascade" {
		t.Errorf("onDelete = %q", rel.OnDelete)
	}
	if rel.OnUpdate != "NoAction" {
		t.Errorf("onUpdate = %q", rel.OnUpdate)
	}
	if rel.Type != ir.RelationManyToOne {
		t.Errorf("expected ManyToOne, got %v", rel.Type)
	}

	user := schema.Models[1]
	if len(user.Relations) != 1 {
		t.Fatalf("expected 1 relation on User, got %d", len(user.Relations))
	}
	if user.Relations[0].Type != ir.RelationOneToOne {
		t.Errorf("expected OneToOne, got %v", user.Relations[0].Type)
	}
}

func TestResolveRelationNameKeyword(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{
			{
				Name: "Post",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					{
						Name: "author",
						Type: ast.FieldType{Name: "User"},
						Attributes: []ast.Attribute{{
							Name: "relation",
							Args: []ast.AttributeArg{
								{Name: "name", Value: ast.StringLiteral{Value: "PostAuthor"}},
							},
						}},
					},
				},
			},
			{
				Name:   "User",
				Fields: []ast.FieldDecl{{Name: "id", Type: ast.FieldType{Name: "Int"}}},
			},
		},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := schema.Models[0].Relations[0].Name; got != "PostAuthor" {
		t.Errorf("relation name = %q, want %q", got, "PostAuthor")
	}
}

// ---------------------------------------------------------------------------
// 22. Self-referencing model (circular reference resolves fine)
// ---------------------------------------------------------------------------

func TestResolveSelfReference(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "Category",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "parentId", Type: ast.FieldType{Name: "Int", IsOptional: true}},
				{
					Name: "parent",
					Type: ast.FieldType{Name: "Category", IsOptional: true},
					Attributes: []ast.Attribute{{
						Name: "relation",
						Args: []ast.AttributeArg{
							{Name: "fields", Value: ast.ArrayLiteral{Elements: []ast.Expression{ast.Identifier{Name: "parentId"}}}},
							{Name: "references", Value: ast.ArrayLiteral{Elements: []ast.Expression{ast.Identifier{Name: "id"}}}},
						},
					}},
				},
				{Name: "children", Type: ast.FieldType{Name: "Category", IsList: true}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	m := schema.Models[0]
	if len(m.Relations) != 2 {
		t.Fatalf("expected 2 relations, got %d", len(m.Relations))
	}
	if m.Relations[0].FromModel != "Category" || m.Relations[0].ToModel != "Category" {
		t.Errorf("self relation = %+v", m.Relations[0])
	}
	if m.Relations[1].Type != ir.RelationOneToMany {
		t.Errorf("expected OneToMany for children, got %v", m.Relations[1].Type)
	}
}

// ---------------------------------------------------------------------------
// 23. References across documents in the DocumentSet
// ---------------------------------------------------------------------------

func TestResolveAcrossDocuments(t *testing.T) {
	ds := &ast.DocumentSet{Documents: []*ast.Document{
		{Models: []ast.ModelDecl{{
			Name: "Post",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "author", Type: ast.FieldType{Name: "User"}},
			},
		}}},
		{Models: []ast.ModelDecl{{
			Name:   "User",
			Fields: []ast.FieldDecl{{Name: "id", Type: ast.FieldType{Name: "Int"}}},
		}}},
	}}

	schema, errs := Resolve(ds)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(schema.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(schema.Models))
	}
	if schema.Models[0].Fields[1].Type != ir.FieldKindRelation {
		t.Errorf("expected relation across documents, got %v", schema.Models[0].Fields[1].Type)
	}
}

// ---------------------------------------------------------------------------
// 24. Relation field referencing an undeclared type
// ---------------------------------------------------------------------------

func TestResolveUnknownRelationTarget(t *testing.T) {
	ds := docSet(&ast.Document{
		Models: []ast.ModelDecl{{
			Name: "Post",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "author", Type: ast.FieldType{Name: "Ghost"}},
			},
		}},
	})

	schema, errs := Resolve(ds)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	// Falls back to a scalar field and produces no relation.
	if schema.Models[0].Fields[1].Type != ir.FieldKindScalar {
		t.Errorf("expected scalar fallback, got %v", schema.Models[0].Fields[1].Type)
	}
	if len(schema.Models[0].Relations) != 0 {
		t.Errorf("expected 0 relations, got %d", len(schema.Models[0].Relations))
	}
}

// ---------------------------------------------------------------------------
// 25. Index helper edge cases
// ---------------------------------------------------------------------------

func TestIndexHasExpressionNil(t *testing.T) {
	if indexHasExpression(nil) {
		t.Error("expected false for nil index")
	}
}

func TestIndexOptionAt(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		idx    int
		want   string
	}{
		{"empty values", nil, 0, ""},
		{"single value broadcasts", []string{"Desc"}, 5, "Desc"},
		{"within bounds", []string{"a", "b"}, 1, "b"},
		{"out of bounds", []string{"a", "b"}, 3, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := indexOptionAt(tc.values, tc.idx); got != tc.want {
				t.Errorf("indexOptionAt(%v, %d) = %q, want %q", tc.values, tc.idx, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 26. Expression helper edge cases
// ---------------------------------------------------------------------------

func TestExprToString(t *testing.T) {
	cases := []struct {
		name string
		expr ast.Expression
		want string
	}{
		{"string", ast.StringLiteral{Value: "s"}, "s"},
		{"number", ast.NumberLiteral{Value: "1.5"}, "1.5"},
		{"boolean", ast.BooleanLiteral{Value: false}, "false"},
		{"identifier", ast.Identifier{Name: "Cascade"}, "Cascade"},
		{"function call", ast.FunctionCall{Name: "now"}, "now(...)"},
		{"unsupported", ast.ArrayLiteral{}, ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exprToString(tc.expr); got != tc.want {
				t.Errorf("exprToString() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExprToBool(t *testing.T) {
	if !exprToBool(ast.BooleanLiteral{Value: true}) {
		t.Error("expected true for BooleanLiteral(true)")
	}
	if exprToBool(ast.BooleanLiteral{Value: false}) {
		t.Error("expected false for BooleanLiteral(false)")
	}
	// Non-boolean expression falls back to string comparison.
	if !exprToBool(ast.Identifier{Name: "true"}) {
		t.Error("expected true for Identifier(true)")
	}
	if exprToBool(ast.Identifier{Name: "yes"}) {
		t.Error("expected false for Identifier(yes)")
	}
}

func TestExprToStringSlice(t *testing.T) {
	cases := []struct {
		name string
		expr ast.Expression
		want []string
	}{
		{"array", ast.ArrayLiteral{Elements: []ast.Expression{
			ast.Identifier{Name: "a"},
			ast.StringLiteral{Value: "b"},
		}}, []string{"a", "b"}},
		{"identifier", ast.Identifier{Name: "solo"}, []string{"solo"}},
		{"string literal", ast.StringLiteral{Value: "one"}, []string{"one"}},
		{"function call", ast.FunctionCall{Name: "now"}, []string{"now(...)"}},
		{"empty result", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exprToStringSlice(tc.expr)
			if len(got) != len(tc.want) {
				t.Fatalf("exprToStringSlice() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("exprToStringSlice() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
