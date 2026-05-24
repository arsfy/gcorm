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
	if len(m.Fields) != 3 {
		t.Fatalf("expected 3 fields, got %d", len(m.Fields))
	}
	// id field
	f := m.Fields[0]
	if f.Type != ir.FieldKindScalar || f.ScalarType != "Int" {
		t.Errorf("field[0] type = %v, scalar = %q", f.Type, f.ScalarType)
	}
	// email field
	f = m.Fields[1]
	if f.Type != ir.FieldKindScalar || f.ScalarType != "String" {
		t.Errorf("field[1] type = %v, scalar = %q", f.Type, f.ScalarType)
	}
	// active field
	f = m.Fields[2]
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
