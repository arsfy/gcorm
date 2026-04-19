package golang

import (
	"strings"
	"testing"

	"github.com/arsfy/gco-orm/pkg/schema/ir"
)

func testSchema() *ir.Schema {
	return &ir.Schema{
		Datasource: &ir.Datasource{
			Name:     "db",
			Provider: "postgresql",
			URL:      "postgresql://localhost:5432/test",
		},
		Generators: []*ir.Generator{
			{
				Name:    "go",
				Package: "db",
				Output:  "./gen",
			},
		},
		Models: []*ir.Model{
			{
				Name:   "User",
				DBName: "users",
				Fields: []*ir.Field{
					{
						Name:       "id",
						Type:       ir.FieldKindScalar,
						ScalarType: "String",
						IsID:       true,
						Default: &ir.DefaultValue{
							IsFunction: true,
							FuncName:   "uuid",
						},
					},
					{
						Name:       "email",
						Type:       ir.FieldKindScalar,
						ScalarType: "String",
						IsUnique:   true,
					},
					{
						Name:       "name",
						Type:       ir.FieldKindScalar,
						ScalarType: "String",
						IsOptional: true,
					},
					{
						Name:       "age",
						Type:       ir.FieldKindScalar,
						ScalarType: "Int",
					},
					{
						Name:       "createdAt",
						Type:       ir.FieldKindScalar,
						ScalarType: "DateTime",
					},
					{
						Name:      "posts",
						Type:      ir.FieldKindRelation,
						ModelType: "Post",
						IsList:    true,
					},
				},
				PrimaryKey: &ir.PrimaryKey{
					Fields: []string{"id"},
				},
			},
			{
				Name: "Post",
				Fields: []*ir.Field{
					{
						Name:       "id",
						Type:       ir.FieldKindScalar,
						ScalarType: "String",
						IsID:       true,
					},
					{
						Name:       "title",
						Type:       ir.FieldKindScalar,
						ScalarType: "String",
					},
					{
						Name:       "content",
						Type:       ir.FieldKindScalar,
						ScalarType: "String",
						IsOptional: true,
					},
					{
						Name:       "published",
						Type:       ir.FieldKindScalar,
						ScalarType: "Boolean",
						Default: &ir.DefaultValue{
							IsBool: true,
							Value:  "false",
						},
					},
					{
						Name:       "authorId",
						Type:       ir.FieldKindScalar,
						ScalarType: "String",
					},
					{
						Name:       "author",
						Type:       ir.FieldKindRelation,
						ModelType:  "User",
						IsOptional: false,
					},
				},
				PrimaryKey: &ir.PrimaryKey{
					Fields: []string{"id"},
				},
			},
		},
		Enums: []*ir.Enum{
			{
				Name: "Role",
				Values: []*ir.EnumValue{
					{Name: "USER"},
					{Name: "ADMIN"},
				},
			},
		},
	}
}

func TestNewGenerator(t *testing.T) {
	s := testSchema()
	g := NewGenerator(s)
	if g.pkg != "db" {
		t.Errorf("pkg = %q, want %q", g.pkg, "db")
	}
	if g.output != "./gen" {
		t.Errorf("output = %q, want %q", g.output, "./gen")
	}
	if g.dialect != "postgresql" {
		t.Errorf("dialect = %q, want %q", g.dialect, "postgresql")
	}
}

func TestGenerate(t *testing.T) {
	s := testSchema()
	g := NewGenerator(s)
	files, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	if len(files) == 0 {
		t.Fatal("Generate() produced no files")
	}

	paths := make(map[string]bool)
	for _, f := range files {
		paths[f.Path] = true
		if len(f.Content) == 0 {
			t.Errorf("file %s has empty content", f.Path)
		}
	}

	expectedPaths := []string{
		"model/enums.go",
		"model/models.go",
		"query/post.go",
		"query/user.go",
		"client/client.go",
	}
	for _, p := range expectedPaths {
		if !paths[p] {
			t.Errorf("missing expected file %s", p)
		}
	}
}

func TestGenerateModelsContent(t *testing.T) {
	s := testSchema()
	g := NewGenerator(s)
	files, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	var modelsContent string
	for _, f := range files {
		if f.Path == "model/models.go" {
			modelsContent = string(f.Content)
			break
		}
	}

	if modelsContent == "" {
		t.Fatal("model/models.go not found")
	}

	// Check model struct declarations.
	if !strings.Contains(modelsContent, "type User struct") {
		t.Error("models.go should contain User struct")
	}
	if !strings.Contains(modelsContent, "type Post struct") {
		t.Error("models.go should contain Post struct")
	}
	// Check it has the DO NOT EDIT header.
	if !strings.Contains(modelsContent, "DO NOT EDIT") {
		t.Error("models.go should have DO NOT EDIT header")
	}
}

func TestGenerateEnumsContent(t *testing.T) {
	s := testSchema()
	g := NewGenerator(s)
	files, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	var enumsContent string
	for _, f := range files {
		if f.Path == "model/enums.go" {
			enumsContent = string(f.Content)
			break
		}
	}

	if enumsContent == "" {
		t.Fatal("model/enums.go not found")
	}

	if !strings.Contains(enumsContent, "type Role string") {
		t.Error("enums.go should contain Role type")
	}
	if !strings.Contains(enumsContent, `RoleUSER`) {
		t.Error("enums.go should contain RoleUSER constant")
	}
	if !strings.Contains(enumsContent, `RoleADMIN`) {
		t.Error("enums.go should contain RoleADMIN constant")
	}
}

func TestGenerateQueryContent(t *testing.T) {
	s := testSchema()
	g := NewGenerator(s)
	files, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	var userQueryContent string
	for _, f := range files {
		if f.Path == "query/user.go" {
			userQueryContent = string(f.Content)
			break
		}
	}

	if userQueryContent == "" {
		t.Fatal("query/user.go not found")
	}

	// Check query builder namespace.
	if !strings.Contains(userQueryContent, "UserQuery") {
		t.Error("user.go should contain UserQuery type")
	}
	// Check field query helpers exist.
	if !strings.Contains(userQueryContent, "Equals") {
		t.Error("user.go should contain Equals method")
	}
	if !strings.Contains(userQueryContent, "WhereClause") {
		t.Error("user.go should contain WhereClause type")
	}
}

func TestGenerateClientContent(t *testing.T) {
	s := testSchema()
	g := NewGenerator(s)
	files, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	var clientContent string
	for _, f := range files {
		if f.Path == "client/client.go" {
			clientContent = string(f.Content)
			break
		}
	}

	if clientContent == "" {
		t.Fatal("client/client.go not found")
	}

	if !strings.Contains(clientContent, "type Client struct") {
		t.Error("client.go should contain Client struct")
	}
	if !strings.Contains(clientContent, "func New(") {
		t.Error("client.go should contain New constructor")
	}
	if !strings.Contains(clientContent, "func (c *Client) Tx(") {
		t.Error("client.go should contain Tx method")
	}
	if !strings.Contains(clientContent, "FindMany") {
		t.Error("client.go should contain FindMany")
	}
	if !strings.Contains(clientContent, "CreateOne") {
		t.Error("client.go should contain CreateOne")
	}
	if !strings.Contains(clientContent, "DeleteMany") {
		t.Error("client.go should contain DeleteMany")
	}
}

func TestManifest(t *testing.T) {
	s := testSchema()
	g := NewGenerator(s)
	m := g.Manifest()

	if m.Generator != "gco-go" {
		t.Errorf("Generator = %q, want %q", m.Generator, "gco-go")
	}
	if m.Package != "db" {
		t.Errorf("Package = %q, want %q", m.Package, "db")
	}
	if len(m.Models) != 2 {
		t.Errorf("Models count = %d, want 2", len(m.Models))
	}
	if len(m.Enums) != 1 {
		t.Errorf("Enums count = %d, want 1", len(m.Enums))
	}
}

func TestGenerateDeterministic(t *testing.T) {
	s := testSchema()
	g := NewGenerator(s)

	files1, err := g.Generate()
	if err != nil {
		t.Fatalf("first Generate() error: %v", err)
	}

	files2, err := g.Generate()
	if err != nil {
		t.Fatalf("second Generate() error: %v", err)
	}

	if len(files1) != len(files2) {
		t.Fatalf("file counts differ: %d vs %d", len(files1), len(files2))
	}

	for i := range files1 {
		if files1[i].Path != files2[i].Path {
			t.Errorf("path %d: %q vs %q", i, files1[i].Path, files2[i].Path)
		}
		if string(files1[i].Content) != string(files2[i].Content) {
			t.Errorf("content differs for %s", files1[i].Path)
		}
	}
}

func TestGoTypeForField(t *testing.T) {
	tests := []struct {
		name  string
		field *ir.Field
		want  string
	}{
		{
			name:  "string",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String"},
			want:  "string",
		},
		{
			name:  "optional string",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "String", IsOptional: true},
			want:  "*string",
		},
		{
			name:  "int",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "Int"},
			want:  "int",
		},
		{
			name:  "bigint",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "BigInt"},
			want:  "int64",
		},
		{
			name:  "float",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "Float"},
			want:  "float64",
		},
		{
			name:  "boolean",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "Boolean"},
			want:  "bool",
		},
		{
			name:  "datetime",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "DateTime"},
			want:  "time.Time",
		},
		{
			name:  "enum",
			field: &ir.Field{Type: ir.FieldKindEnum, EnumType: "Role"},
			want:  "Role",
		},
		{
			name:  "relation",
			field: &ir.Field{Type: ir.FieldKindRelation, ModelType: "Post"},
			want:  "*Post",
		},
		{
			name:  "relation list",
			field: &ir.Field{Type: ir.FieldKindRelation, ModelType: "Post", IsList: true},
			want:  "[]*Post",
		},
		{
			name:  "bytes",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "Bytes"},
			want:  "[]byte",
		},
		{
			name:  "uuid",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "UUID"},
			want:  "string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := goTypeForField(tt.field)
			if got != tt.want {
				t.Errorf("goTypeForField() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHelpers(t *testing.T) {
	if got := toLowerFirst("User"); got != "user" {
		t.Errorf("toLowerFirst(User) = %q, want %q", got, "user")
	}
	if got := toUpperFirst("user"); got != "User" {
		t.Errorf("toUpperFirst(user) = %q, want %q", got, "User")
	}
	if got := toSnakeCase("createdAt"); got != "created_at" {
		t.Errorf("toSnakeCase(createdAt) = %q, want %q", got, "created_at")
	}
	if got := simplePluralize("User"); got != "Users" {
		t.Errorf("pluralize(User) = %q, want %q", got, "Users")
	}
	if got := simplePluralize("Category"); got != "Categories" {
		t.Errorf("pluralize(Category) = %q, want %q", got, "Categories")
	}
}

func TestGenerateEmptySchema(t *testing.T) {
	s := &ir.Schema{}
	g := NewGenerator(s)
	files, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	// Should produce just the client file.
	if len(files) != 1 {
		t.Errorf("expected 1 file for empty schema, got %d", len(files))
	}
}
