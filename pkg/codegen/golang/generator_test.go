package golang

import (
	"regexp"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ir"
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
						Name:     "role",
						Type:     ir.FieldKindEnum,
						EnumType: "Role",
					},
					{
						Name:       "age",
						Type:       ir.FieldKindScalar,
						ScalarType: "Int",
					},
					{
						Name:       "rank",
						Type:       ir.FieldKindScalar,
						ScalarType: "SmallInt",
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

func TestRenderTemplateReturnsFormatError(t *testing.T) {
	g := NewGenerator(testSchema())
	_, err := g.renderTemplate("invalid", "package invalid-", nil)
	if err == nil {
		t.Fatal("renderTemplate() should report invalid generated Go source")
	}
	if !strings.Contains(err.Error(), "format generated template invalid") {
		t.Fatalf("renderTemplate() error = %q", err)
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
	if !regexp.MustCompile(`(?m)\bRank\s+int16\b`).MatchString(modelsContent) {
		t.Error("models.go should map SmallInt fields to int16")
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
	if !strings.Contains(userQueryContent, "var User = UserQuery{") {
		t.Error("user.go should expose a User query namespace value")
	}
	if !regexp.MustCompile(`(?m)\bEmail\s+userEmailField\b`).MatchString(userQueryContent) {
		t.Error("user.go should scope field helpers under UserQuery")
	}
	if !strings.Contains(userQueryContent, "func (userNameField) Set(v string) UserSetClause") {
		t.Error("user.go should allow plain values in Set for optional string fields")
	}
	if !strings.Contains(userQueryContent, "func (userNameField) SetNull() UserSetClause") {
		t.Error("user.go should generate SetNull for optional fields")
	}
	if strings.Contains(userQueryContent, "var UserEmail =") {
		t.Error("user.go should not generate flattened UserEmail helper vars")
	}
	// Check field query helpers exist.
	if !strings.Contains(userQueryContent, "Equals") {
		t.Error("user.go should contain Equals method")
	}
	if !strings.Contains(userQueryContent, "WhereClause") {
		t.Error("user.go should contain WhereClause type")
	}
	if !strings.Contains(userQueryContent, "model.Role") {
		t.Error("user.go should reference model.Role for enum fields")
	}
	if !strings.Contains(userQueryContent, "func (userRankField) Lt(v int16) UserWhereClause") {
		t.Error("user.go should generate numeric comparison helpers for SmallInt")
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
	if !strings.Contains(clientContent, "func (a UserActions) Query() UserQueryBuilder") {
		t.Error("client.go should contain a staged query entry point")
	}
	if !strings.Contains(clientContent, "func (b UserQueryBuilder) Do(ctx context.Context) ([]model.User, error)") {
		t.Error("client.go should contain a Do method for staged queries")
	}
	if !strings.Contains(clientContent, "func (b UserQueryBuilder) Where(clauses ...query.UserWhereClause) UserQueryBuilder") {
		t.Error("client.go should contain staged Where chaining")
	}
	if !strings.Contains(clientContent, "func (a UserActions) Create() UserCreateBuilder") {
		t.Error("client.go should contain a staged create entry point")
	}
	if !strings.Contains(clientContent, "func (b UserCreateBuilder) Do(ctx context.Context) (*model.User, error)") {
		t.Error("client.go should contain a Do method for staged create")
	}
	if !strings.Contains(clientContent, "func (a UserActions) Update() UserUpdateBuilder") {
		t.Error("client.go should contain a staged update entry point")
	}
	if !strings.Contains(clientContent, "func (b UserUpdateBuilder) Do(ctx context.Context) (*model.User, error)") {
		t.Error("client.go should contain a Do method for staged update")
	}
	if !strings.Contains(clientContent, "func (b UserUpdateBuilder) DoMany(ctx context.Context) (int64, error)") {
		t.Error("client.go should contain a DoMany method for staged update")
	}
	if !strings.Contains(clientContent, "func (a UserActions) Delete() UserDeleteBuilder") {
		t.Error("client.go should contain a staged delete entry point")
	}
	if !strings.Contains(clientContent, "func (b UserDeleteBuilder) Do(ctx context.Context) (*model.User, error)") {
		t.Error("client.go should contain a Do method for staged delete")
	}
	if !strings.Contains(clientContent, "func (b UserDeleteBuilder) DoMany(ctx context.Context) (int64, error)") {
		t.Error("client.go should contain a DoMany method for staged delete")
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
			name:  "smallint",
			field: &ir.Field{Type: ir.FieldKindScalar, ScalarType: "SmallInt"},
			want:  "int16",
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

func TestClientDialectMatchesDatasource(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{provider: "postgresql", want: `dialect: "postgresql"`},
		{provider: "mysql", want: `dialect: "mysql"`},
		{provider: "sqlite", want: `dialect: "sqlite"`},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			s := testSchema()
			s.Datasource.Provider = tt.provider
			g := NewGenerator(s)
			files, err := g.Generate()
			if err != nil {
				t.Fatalf("Generate() error: %v", err)
			}
			client := fileContent(t, files, "client/client.go")
			if !strings.Contains(client, tt.want) {
				t.Errorf("client.go should default dialect from datasource provider; want %q", tt.want)
			}
		})
	}
}

func TestGenerateNameConflicts(t *testing.T) {
	scalarField := func(name string) *ir.Field {
		return &ir.Field{Name: name, Type: ir.FieldKindScalar, ScalarType: "String"}
	}
	modelWithFields := func(name string, fields ...*ir.Field) *ir.Model {
		return &ir.Model{Name: name, Fields: fields}
	}

	tests := []struct {
		name    string
		models  []*ir.Model
		wantErr string
	}{
		{
			name: "model name collides with derived suffix type",
			models: []*ir.Model{
				modelWithFields("User", scalarField("id")),
				modelWithFields("UserQuery", scalarField("id")),
			},
			wantErr: `conflicts with generated type "UserQuery"`,
		},
		{
			name: "model name collides with derived query config type",
			models: []*ir.Model{
				modelWithFields("User", scalarField("id")),
				modelWithFields("UserQueryConfig", scalarField("id")),
			},
			wantErr: `conflicts with generated type "UserQueryConfig"`,
		},
		{
			name: "model name collides with derived create input type",
			models: []*ir.Model{
				modelWithFields("User", scalarField("id")),
				modelWithFields("UserCreateInput", scalarField("id")),
			},
			wantErr: `conflicts with generated type "UserCreateInput"`,
		},
		{
			name: "cross-model field helper collision",
			models: []*ir.Model{
				modelWithFields("User", scalarField("nameX")),
				modelWithFields("UserName", scalarField("x")),
			},
			wantErr: "both generate helper type",
		},
		{
			name: "model names differing only in case",
			models: []*ir.Model{
				modelWithFields("User", scalarField("id")),
				modelWithFields("USER", scalarField("id")),
			},
			wantErr: "collide",
		},
		{
			name: "duplicate model names",
			models: []*ir.Model{
				modelWithFields("User", scalarField("id")),
				modelWithFields("User", scalarField("id")),
			},
			wantErr: "collide",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &ir.Schema{
				Datasource: &ir.Datasource{Provider: "postgresql"},
				Models:     tt.models,
			}
			g := NewGenerator(s)
			_, err := g.Generate()
			if err == nil {
				t.Fatal("Generate() should report the name conflict before generating code")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Generate() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestGenerateOptionalScalarsNoDoublePointer(t *testing.T) {
	s := &ir.Schema{
		Datasource: &ir.Datasource{Provider: "postgresql"},
		Models: []*ir.Model{
			{
				Name: "Account",
				Fields: []*ir.Field{
					{Name: "id", Type: ir.FieldKindScalar, ScalarType: "String", IsID: true},
					{Name: "name", Type: ir.FieldKindScalar, ScalarType: "String", IsOptional: true},
					{Name: "nickname", Type: ir.FieldKindScalar, ScalarType: "String", IsOptional: true, IsUnique: true},
				},
				PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
			},
		},
	}
	g := NewGenerator(s)
	files, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	query := fileContent(t, files, "query/account.go")

	if strings.Contains(query, "**") {
		t.Errorf("query/account.go should not contain double pointers:\n%s", query)
	}
	if !regexp.MustCompile(`(?m)\bName\s+\*string\b`).MatchString(query) {
		t.Error("CreateInput should use *string for optional scalar fields")
	}
	if !regexp.MustCompile(`(?m)\bNickname\s+\*string\b`).MatchString(query) {
		t.Error("WhereUniqueInput should use *string for optional unique fields")
	}
}

func TestGenerateMappedNamesAreQuoted(t *testing.T) {
	s := &ir.Schema{
		Datasource: &ir.Datasource{Provider: "postgresql"},
		Models: []*ir.Model{
			{
				Name:   "Mapped",
				DBName: `odd"table`,
				Fields: []*ir.Field{
					{Name: "id", Type: ir.FieldKindScalar, ScalarType: "String", IsID: true},
					{Name: "oddCol", DBName: `odd"col`, Type: ir.FieldKindScalar, ScalarType: "String"},
				},
				PrimaryKey: &ir.PrimaryKey{Fields: []string{"id"}},
			},
		},
	}
	g := NewGenerator(s)
	files, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	models := fileContent(t, files, "model/models.go")
	if !strings.Contains(models, `db:"odd\"col"`) {
		t.Errorf("models.go should escape @map column names in struct tags:\n%s", models)
	}

	query := fileContent(t, files, "query/mapped.go")
	if !strings.Contains(query, `const MappedTable = "odd\"table"`) {
		t.Errorf("query/mapped.go should escape @@map table names:\n%s", query)
	}
	if !strings.Contains(query, `const MappedOddColColumn = "odd\"col"`) {
		t.Errorf("query/mapped.go should escape @map column constants:\n%s", query)
	}
	if !strings.Contains(query, `Field: "odd\"col"`) {
		t.Errorf("query/mapped.go should escape column names in where clauses:\n%s", query)
	}

	client := fileContent(t, files, "client/client.go")
	if !strings.Contains(client, `case "odd\"col":`) {
		t.Errorf("client.go should escape column names in field quoting switch:\n%s", client)
	}
}

func TestGenerateRequiresModulePath(t *testing.T) {
	t.Chdir(t.TempDir()) // no go.mod in this directory tree

	s := &ir.Schema{
		Datasource: &ir.Datasource{Provider: "postgresql"},
		Models: []*ir.Model{
			{
				Name: "User",
				Fields: []*ir.Field{
					{Name: "id", Type: ir.FieldKindScalar, ScalarType: "String", IsID: true},
				},
			},
		},
	}

	t.Run("errors without go.mod or WithModulePath", func(t *testing.T) {
		g := NewGenerator(s)
		_, err := g.Generate()
		if err == nil {
			t.Fatal("Generate() should fail when the module path cannot be determined")
		}
		if !strings.Contains(err.Error(), "cannot determine the Go module path") {
			t.Fatalf("Generate() error = %q", err)
		}
	})

	t.Run("succeeds with WithModulePath", func(t *testing.T) {
		g := NewGenerator(s, WithModulePath("example.com/acme/gen"))
		files, err := g.Generate()
		if err != nil {
			t.Fatalf("Generate() error: %v", err)
		}
		client := fileContent(t, files, "client/client.go")
		if !strings.Contains(client, `"example.com/acme/gen/model"`) {
			t.Errorf("client.go should import the configured module path:\n%s", client)
		}
	})

	t.Run("empty schema does not require a module path", func(t *testing.T) {
		g := NewGenerator(&ir.Schema{})
		if _, err := g.Generate(); err != nil {
			t.Fatalf("Generate() error for empty schema: %v", err)
		}
	})
}
