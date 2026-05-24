package validator

import (
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func pos(line, col int) ast.Position {
	return ast.Position{Line: line, Column: col}
}

func span(line, col int) ast.Span {
	return ast.Span{Start: pos(line, col)}
}

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

func posArg(val ast.Expression) ast.AttributeArg {
	return ast.AttributeArg{Value: val}
}

func namedArg(name string, val ast.Expression) ast.AttributeArg {
	return ast.AttributeArg{Name: name, Value: val}
}

func requireNoErrors(t *testing.T, r *ValidationResult) {
	t.Helper()
	if r.HasErrors() {
		var msgs []string
		for _, e := range r.Errors {
			msgs = append(msgs, e.Error())
		}
		t.Fatalf("unexpected errors:\n  %s", strings.Join(msgs, "\n  "))
	}
}

func requireErrors(t *testing.T, r *ValidationResult, substr string) {
	t.Helper()
	if !r.HasErrors() {
		t.Fatal("expected errors but got none")
	}
	for _, e := range r.Errors {
		if strings.Contains(e.Message, substr) {
			return
		}
	}
	var msgs []string
	for _, e := range r.Errors {
		msgs = append(msgs, e.Message)
	}
	t.Fatalf("expected error containing %q, got:\n  %s", substr, strings.Join(msgs, "\n  "))
}

func requireWarnings(t *testing.T, r *ValidationResult, substr string) {
	t.Helper()
	for _, w := range r.Warnings {
		if strings.Contains(w.Message, substr) {
			return
		}
	}
	t.Fatalf("expected warning containing %q, got none", substr)
}

// validDatasource returns a datasource with valid provider and url.
func validDatasource() ast.DatasourceDecl {
	return ast.DatasourceDecl{
		Name: "db",
		Entries: []ast.ConfigEntry{
			{Key: "provider", Value: strLit("postgresql")},
			{Key: "url", Value: strLit("postgres://localhost/test")},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestValidSimpleSchema(t *testing.T) {
	doc := &ast.Document{
		Datasources: []ast.DatasourceDecl{validDatasource()},
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}, Attributes: []ast.Attribute{{Name: "id"}}},
				{Name: "email", Type: ast.FieldType{Name: "String"}},
			},
		}},
		Enums: []ast.EnumDecl{{
			Name:   "Role",
			Values: []ast.EnumValue{{Name: "ADMIN"}, {Name: "USER"}},
		}},
	}
	r := Validate(doc)
	requireNoErrors(t, r)
}

func TestDuplicateModelNames(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{
			{Name: "User", Fields: []ast.FieldDecl{{Name: "id", Type: ast.FieldType{Name: "Int"}}}},
			{Name: "User", Fields: []ast.FieldDecl{{Name: "id", Type: ast.FieldType{Name: "Int"}}}, Span: ast.Span{Start: pos(5, 1)}},
		},
	}
	r := Validate(doc)
	requireErrors(t, r, "duplicate model name")
}

func TestDuplicateFieldNames(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "email", Type: ast.FieldType{Name: "String"}},
				{Name: "email", Type: ast.FieldType{Name: "String"}, Span: span(3, 3)},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "duplicate field name")
}

func TestInvalidFieldType(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "Foo",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "bar", Type: ast.FieldType{Name: "UnknownType", Span: span(3, 10)}},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "unknown type")
}

func TestMultipleDatasources(t *testing.T) {
	doc := &ast.Document{
		Datasources: []ast.DatasourceDecl{
			{Name: "db1", Entries: []ast.ConfigEntry{{Key: "url", Value: strLit("x")}}},
			{Name: "db2", Entries: []ast.ConfigEntry{{Key: "url", Value: strLit("y")}}, Span: span(5, 1)},
		},
	}
	r := Validate(doc)
	requireErrors(t, r, "multiple datasource blocks")
}

func TestInvalidProvider(t *testing.T) {
	doc := &ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit("mongodb"), Span: span(2, 3)},
				{Key: "url", Value: strLit("x")},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "invalid provider")
}

func TestMissingDatasourceURL(t *testing.T) {
	doc := &ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit("postgresql")},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "requires a \"url\" entry")
}

func TestIdOnMultipleFields(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}, Attributes: []ast.Attribute{{Name: "id"}}},
				{Name: "uuid", Type: ast.FieldType{Name: "String"}, Attributes: []ast.Attribute{{Name: "id"}}},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "@id on multiple fields")
}

func TestModelLevelIdWithArray(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "CompositeKey",
			Fields: []ast.FieldDecl{
				{Name: "tenantId", Type: ast.FieldType{Name: "String"}},
				{Name: "itemId", Type: ast.FieldType{Name: "String"}},
			},
			Attributes: []ast.Attribute{{
				Name:         "id",
				IsModelLevel: true,
				Args:         []ast.AttributeArg{posArg(arrayLit(ident("tenantId"), ident("itemId")))},
			}},
		}},
	}
	r := Validate(doc)
	requireNoErrors(t, r)
}

func TestDefaultWithWrongArgCount(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "Foo",
			Fields: []ast.FieldDecl{
				{
					Name: "name",
					Type: ast.FieldType{Name: "String"},
					Attributes: []ast.Attribute{
						{Name: "default", Span: span(2, 20)},
					},
				},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "@default requires exactly one argument")
}

func TestUpdatedAtOnNonDateTime(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "Foo",
			Fields: []ast.FieldDecl{
				{
					Name: "name",
					Type: ast.FieldType{Name: "String"},
					Attributes: []ast.Attribute{
						{Name: "updatedAt", Span: span(2, 20)},
					},
				},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "@updatedAt is only valid on DateTime")
}

func TestRelationMissingFieldsReferences(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{
			{
				Name: "Post",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					{
						Name: "author",
						Type: ast.FieldType{Name: "User"},
						Attributes: []ast.Attribute{
							{Name: "relation", Span: span(4, 20)},
						},
					},
				},
			},
			{
				Name: "User",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
				},
			},
		},
	}
	r := Validate(doc)
	requireErrors(t, r, "@relation requires a \"fields\" argument")
	requireErrors(t, r, "@relation requires a \"references\" argument")
}

func TestRelationOnScalarField(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{
			{
				Name: "Post",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					{
						Name: "authorId",
						Type: ast.FieldType{Name: "Int"},
						Attributes: []ast.Attribute{
							{
								Name: "relation",
								Args: []ast.AttributeArg{
									namedArg("fields", arrayLit(ident("authorId"))),
									namedArg("references", arrayLit(ident("id"))),
								},
								Span: span(3, 20),
							},
						},
					},
				},
			},
		},
	}
	r := Validate(doc)
	requireErrors(t, r, "@relation is only valid on fields with a model type")
}

func TestEnumDuplicateValues(t *testing.T) {
	doc := &ast.Document{
		Enums: []ast.EnumDecl{{
			Name: "Role",
			Values: []ast.EnumValue{
				{Name: "ADMIN"},
				{Name: "ADMIN", Span: span(4, 3)},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "duplicate enum value")
}

func TestModelNameLowercase(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name:   "user",
			Fields: []ast.FieldDecl{{Name: "id", Type: ast.FieldType{Name: "Int"}}},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "must start with an uppercase letter")
}

func TestFieldNameUppercase(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "Id", Type: ast.FieldType{Name: "Int"}, Span: span(2, 3)},
			},
		}},
	}
	r := Validate(doc)
	requireErrors(t, r, "must start with a lowercase letter")
}

func TestIndexWithFieldReferences(t *testing.T) {
	doc := &ast.Document{
		Models: []ast.ModelDecl{{
			Name: "User",
			Fields: []ast.FieldDecl{
				{Name: "id", Type: ast.FieldType{Name: "Int"}},
				{Name: "email", Type: ast.FieldType{Name: "String"}},
			},
			Attributes: []ast.Attribute{{
				Name:         "index",
				IsModelLevel: true,
				Args:         []ast.AttributeArg{posArg(arrayLit(ident("email")))},
			}},
		}},
	}
	r := Validate(doc)
	requireNoErrors(t, r)
}

func TestValidateDocumentSetCrossFileReferences(t *testing.T) {
	ds := &ast.DocumentSet{
		Documents: []*ast.Document{
			{
				Datasources: []ast.DatasourceDecl{validDatasource()},
				Models: []ast.ModelDecl{{
					Name: "User",
					Fields: []ast.FieldDecl{
						{Name: "id", Type: ast.FieldType{Name: "Int"}, Attributes: []ast.Attribute{{Name: "id"}}},
						{Name: "email", Type: ast.FieldType{Name: "String"}},
					},
				}},
			},
			{
				Models: []ast.ModelDecl{{
					Name: "Post",
					Fields: []ast.FieldDecl{
						{Name: "id", Type: ast.FieldType{Name: "Int"}, Attributes: []ast.Attribute{{Name: "id"}}},
						{Name: "title", Type: ast.FieldType{Name: "String"}},
						{
							Name: "author",
							Type: ast.FieldType{Name: "User"},
							Attributes: []ast.Attribute{{
								Name: "relation",
								Args: []ast.AttributeArg{
									namedArg("fields", arrayLit(ident("authorId"))),
									namedArg("references", arrayLit(ident("id"))),
								},
							}},
						},
						{Name: "authorId", Type: ast.FieldType{Name: "Int"}},
					},
				}},
			},
		},
		Files: []string{"schema.prisma", "post.prisma"},
	}
	r := ValidateDocumentSet(ds)
	requireNoErrors(t, r)
}

func TestValidateDocumentSetMissingType(t *testing.T) {
	ds := &ast.DocumentSet{
		Documents: []*ast.Document{
			{
				Models: []ast.ModelDecl{{
					Name: "Post",
					Fields: []ast.FieldDecl{
						{Name: "id", Type: ast.FieldType{Name: "Int"}},
						{Name: "author", Type: ast.FieldType{Name: "MissingModel", Span: span(3, 10)}},
					},
				}},
			},
		},
		Files: []string{"post.prisma"},
	}
	r := ValidateDocumentSet(ds)
	requireErrors(t, r, "unknown type")
}

func TestWarningsForUnknownConfigKeys(t *testing.T) {
	doc := &ast.Document{
		Datasources: []ast.DatasourceDecl{{
			Name: "db",
			Entries: []ast.ConfigEntry{
				{Key: "provider", Value: strLit("postgresql")},
				{Key: "url", Value: strLit("x")},
				{Key: "customThing", Value: strLit("y"), Span: span(4, 3)},
			},
		}},
	}
	r := Validate(doc)
	requireNoErrors(t, r)
	requireWarnings(t, r, "unknown datasource config key")
}
