package compiler

import (
	"testing"

	"github.com/arsfy/gcorm/pkg/schema/ast"
)

// ---------------------------------------------------------------------------
// 1. Compile valid schema: no errors, schema populated
// ---------------------------------------------------------------------------

func TestCompileValidSchema(t *testing.T) {
	ds := &ast.DocumentSet{
		Documents: []*ast.Document{{
			Datasources: []ast.DatasourceDecl{{
				Name: "db",
				Entries: []ast.ConfigEntry{
					{Key: "provider", Value: ast.StringLiteral{Value: "postgresql"}},
					{Key: "url", Value: ast.StringLiteral{Value: "postgres://localhost/test"}},
				},
			}},
			Generators: []ast.GeneratorDecl{{
				Name: "client",
				Entries: []ast.ConfigEntry{
					{Key: "provider", Value: ast.StringLiteral{Value: "gco-go-client"}},
					{Key: "output", Value: ast.StringLiteral{Value: "./generated"}},
				},
			}},
			Models: []ast.ModelDecl{{
				Name: "User",
				Fields: []ast.FieldDecl{
					{
						Name:       "id",
						Type:       ast.FieldType{Name: "Int"},
						Attributes: []ast.Attribute{{Name: "id"}},
					},
					{Name: "email", Type: ast.FieldType{Name: "String"}},
				},
			}},
			Enums: []ast.EnumDecl{{
				Name:   "Role",
				Values: []ast.EnumValue{{Name: "ADMIN"}, {Name: "USER"}},
			}},
		}},
	}

	result := Compile(ds)
	if result.HasErrors() {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Schema == nil {
		t.Fatal("expected Schema to be non-nil")
	}
	if result.Schema.Datasource == nil {
		t.Error("expected Datasource")
	}
	if len(result.Schema.Generators) != 1 {
		t.Errorf("expected 1 generator, got %d", len(result.Schema.Generators))
	}
	if len(result.Schema.Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(result.Schema.Models))
	}
	if len(result.Schema.Enums) != 1 {
		t.Errorf("expected 1 enum, got %d", len(result.Schema.Enums))
	}
}

// ---------------------------------------------------------------------------
// 2. Compile with validation errors: returns validation errors
// ---------------------------------------------------------------------------

func TestCompileValidationErrors(t *testing.T) {
	// Empty model should cause a validation error.
	ds := &ast.DocumentSet{
		Documents: []*ast.Document{{
			Models: []ast.ModelDecl{{
				Name:   "Empty",
				Fields: []ast.FieldDecl{},
			}},
		}},
	}

	result := Compile(ds)
	if !result.HasErrors() {
		t.Fatal("expected validation errors")
	}
	if result.Validation == nil || !result.Validation.HasErrors() {
		t.Error("expected Validation to contain errors")
	}
	if result.Schema != nil {
		t.Error("expected Schema to be nil when validation fails")
	}
}

// ---------------------------------------------------------------------------
// 3. Compile with resolution errors: returns resolution errors
// ---------------------------------------------------------------------------

func TestCompileResolutionErrors(t *testing.T) {
	// Field references unknown type.
	ds := &ast.DocumentSet{
		Documents: []*ast.Document{{
			Models: []ast.ModelDecl{{
				Name: "Foo",
				Fields: []ast.FieldDecl{
					{Name: "id", Type: ast.FieldType{Name: "Int"}},
					{Name: "bar", Type: ast.FieldType{Name: "UnknownType"}},
				},
			}},
		}},
	}

	result := Compile(ds)
	if !result.HasErrors() {
		t.Fatal("expected resolution errors")
	}
	// Schema may still be partially populated even on resolution errors.
}

// ---------------------------------------------------------------------------
// 4. CompileFile convenience: works for single file
// ---------------------------------------------------------------------------

func TestCompileFile(t *testing.T) {
	// CompileFile creates a minimal DocumentSet; with an empty document
	// it should still run without panicking. It will produce a validation
	// error because there are no models.
	result := CompileFile("schema.prisma", []byte(""))
	// We just verify it does not panic and returns a result.
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}
