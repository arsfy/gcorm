package formatter

import (
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func strLit(s string) ast.StringLiteral {
	return ast.StringLiteral{Value: s}
}

func numLit(s string) ast.NumberLiteral {
	return ast.NumberLiteral{Value: s}
}

func boolLit(b bool) ast.BooleanLiteral {
	return ast.BooleanLiteral{Value: b}
}

func ident(name string) ast.Identifier {
	return ast.Identifier{Name: name}
}

func funcCall(name string, args ...ast.Expression) ast.FunctionCall {
	return ast.FunctionCall{Name: name, Args: args}
}

func arrayLit(elems ...ast.Expression) ast.ArrayLiteral {
	return ast.ArrayLiteral{Elements: elems}
}

func field(name string, typeName string, attrs ...ast.Attribute) ast.FieldDecl {
	return ast.FieldDecl{
		Name:       name,
		Type:       ast.FieldType{Name: typeName},
		Attributes: attrs,
	}
}

func optionalField(name string, typeName string, attrs ...ast.Attribute) ast.FieldDecl {
	return ast.FieldDecl{
		Name:       name,
		Type:       ast.FieldType{Name: typeName, IsOptional: true},
		Attributes: attrs,
	}
}

func listField(name string, typeName string, attrs ...ast.Attribute) ast.FieldDecl {
	return ast.FieldDecl{
		Name:       name,
		Type:       ast.FieldType{Name: typeName, IsList: true},
		Attributes: attrs,
	}
}

func attr(name string, args ...ast.AttributeArg) ast.Attribute {
	return ast.Attribute{Name: name, Args: args}
}

func modelAttr(name string, args ...ast.AttributeArg) ast.Attribute {
	return ast.Attribute{Name: name, IsModelLevel: true, Args: args}
}

func posArg(val ast.Expression) ast.AttributeArg {
	return ast.AttributeArg{Value: val}
}

func namedArg(name string, val ast.Expression) ast.AttributeArg {
	return ast.AttributeArg{Name: name, Value: val}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestEmptyDocument(t *testing.T) {
	got := Format(&ast.Document{})
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}

	got = Format(nil)
	if got != "" {
		t.Errorf("expected empty string for nil, got %q", got)
	}
}

func TestDatasourceFormatting(t *testing.T) {
	doc := &ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit("postgresql")},
				{Key: "url", Value: funcCall("env", strLit("DATABASE_URL"))},
			},
		}},
	}

	want := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}
`
	got := Format(doc)
	if got != want {
		t.Errorf("datasource formatting mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestGeneratorFormatting(t *testing.T) {
	doc := &ast.Document{
		Generators: []ast.GeneratorDecl{{
			Name: "client",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit("gco-orm-go")},
				{Key: "output", Value: strLit("./generated")},
			},
		}},
	}

	want := `generator client {
  provider = "gco-orm-go"
  output   = "./generated"
}
`
	got := Format(doc)
	if got != want {
		t.Errorf("generator formatting mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestModelAlignedFields(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				field("id", "String"),
				field("email", "String"),
				field("createdAt", "DateTime"),
			},
		}},
	}

	want := `model User {
  id        String
  email     String
  createdAt DateTime
}
`
	got := Format(doc)
	if got != want {
		t.Errorf("model aligned fields mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestModelWithAttributes(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				field("id", "String",
					attr("id"),
					attr("default", posArg(funcCall("uuid"))),
				),
				field("email", "String", attr("unique")),
			},
		}},
	}

	want := `model User {
  id    String @id @default(uuid())
  email String @unique
}
`
	got := Format(doc)
	if got != want {
		t.Errorf("model with attributes mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestModelLevelAttributes(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				field("id", "String", attr("id")),
				field("createdAt", "DateTime"),
			},
			Attributes: []ast.Attribute{
				modelAttr("index", posArg(arrayLit(ident("createdAt")))),
				modelAttr("map", posArg(strLit("users"))),
			},
		}},
	}

	want := `model User {
  id        String   @id
  createdAt DateTime

  @@index([createdAt])
  @@map("users")
}
`
	got := Format(doc)
	if got != want {
		t.Errorf("model-level attributes mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestEnumFormatting(t *testing.T) {
	doc := &ast.Document{
		Enums: []ast.EnumDecl{{
			Name: "Role",
			Values: []ast.EnumValue{
				{Name: "USER"},
				{Name: "ADMIN"},
				{Name: "MODERATOR"},
			},
		}},
	}

	want := `enum Role {
  USER
  ADMIN
  MODERATOR
}
`
	got := Format(doc)
	if got != want {
		t.Errorf("enum formatting mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestBlockOrdering(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{
			{Name: "Post"},
			{Name: "Comment"},
		},
		Enums: []ast.EnumDecl{
			{Name: "Status"},
			{Name: "Role"},
		},
		Generators: []ast.GeneratorDecl{
			{Name: "client"},
		},
		Datasources: []ast.DatasourceDecl{
			{Name: "db"},
		},
	}

	got := Format(doc)

	// Check ordering: datasource → generator → enum → model.
	dsIdx := indexOf(got, "datasource db")
	genIdx := indexOf(got, "generator client")
	enumRoleIdx := indexOf(got, "enum Role")
	enumStatusIdx := indexOf(got, "enum Status")
	modelCommentIdx := indexOf(got, "model Comment")
	modelPostIdx := indexOf(got, "model Post")

	if dsIdx < 0 || genIdx < 0 || enumRoleIdx < 0 || enumStatusIdx < 0 || modelCommentIdx < 0 || modelPostIdx < 0 {
		t.Fatalf("missing blocks in output:\n%s", got)
	}

	if dsIdx >= genIdx {
		t.Errorf("datasource should come before generator")
	}
	if genIdx >= enumRoleIdx {
		t.Errorf("generator should come before enums")
	}
	// Alphabetical within groups.
	if enumRoleIdx >= enumStatusIdx {
		t.Errorf("enum Role should come before enum Status")
	}
	if enumStatusIdx >= modelCommentIdx {
		t.Errorf("enums should come before models")
	}
	if modelCommentIdx >= modelPostIdx {
		t.Errorf("model Comment should come before model Post")
	}
}

func TestIdempotency(t *testing.T) {
	doc := &ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit("postgresql")},
				{Key: "url", Value: funcCall("env", strLit("DATABASE_URL"))},
			},
		}},
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				field("id", "String", attr("id")),
				field("email", "String", attr("unique")),
			},
		}},
		Enums: []ast.EnumDecl{{
			Name:   "Role",
			Values: []ast.EnumValue{{Name: "ADMIN"}, {Name: "USER"}},
		}},
	}

	first := Format(doc)
	second := Format(doc)
	if first != second {
		t.Errorf("Format is not idempotent.\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestCommentsPreservation(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Comments: []ast.Comment{
				{Text: "The User model", IsDoc: true},
			},
			Fields: []ast.FieldDecl{
				{
					Name: "id",
					Type: ast.FieldType{Name: "String"},
					Comments: []ast.Comment{
						{Text: "Primary key"},
					},
				},
				field("name", "String"),
			},
		}},
	}

	want := `/// The User model
model User {
  // Primary key
  id   String
  name String
}
`
	got := Format(doc)
	if got != want {
		t.Errorf("comments preservation mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestComplexSchema(t *testing.T) {
	doc := &ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit("postgresql")},
				{Key: "url", Value: funcCall("env", strLit("DATABASE_URL"))},
			},
		}},
		Generators: []ast.GeneratorDecl{{
			Name: "client",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit("gco-orm-go")},
			},
		}},
		Enums: []ast.EnumDecl{{
			Name: "Role",
			Values: []ast.EnumValue{
				{Name: "USER"},
				{Name: "ADMIN"},
			},
			Attributes: []ast.Attribute{
				modelAttr("map", posArg(strLit("roles"))),
			},
		}},
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				field("id", "String",
					attr("id"),
					attr("default", posArg(funcCall("uuid"))),
				),
				field("email", "String", attr("unique")),
				optionalField("name", "String"),
				listField("posts", "Post"),
				field("role", "Role", attr("default", posArg(ident("USER")))),
				field("createdAt", "DateTime", attr("default", posArg(funcCall("now")))),
			},
			Attributes: []ast.Attribute{
				modelAttr("index", posArg(arrayLit(ident("email")))),
				modelAttr("map", posArg(strLit("users"))),
			},
		}},
	}

	want := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

generator client {
  provider = "gco-orm-go"
}

enum Role {
  USER
  ADMIN

  @@map("roles")
}

model User {
  id        String   @id @default(uuid())
  email     String   @unique
  name      String?
  posts     Post[]
  role      Role     @default(USER)
  createdAt DateTime @default(now())

  @@index([email])
  @@map("users")
}
`
	got := Format(doc)
	if got != want {
		t.Errorf("complex schema mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
