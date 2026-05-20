package compiler

import (
	"strings"
	"testing"

	"github.com/arsfy/gco-orm/pkg/codegen/golang"
	"github.com/arsfy/gco-orm/pkg/schema/ir"
	"github.com/arsfy/gco-orm/pkg/schema/parser"
)

// parseAndCompile is a helper that parses schema text and runs the compiler.
func parseAndCompile(t *testing.T, schema string) *CompileResult {
	t.Helper()
	ds, err := parser.ParseMulti(map[string][]byte{"test.gcorm": []byte(schema)})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return Compile(ds)
}

// ---------------------------------------------------------------------------
// 1. Full pipeline: parse → compile → generate
// ---------------------------------------------------------------------------

func TestIntegrationFullPipeline(t *testing.T) {
	schema := `
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

generator go {
  provider = "gco-go"
  output   = "./gen"
  package  = "db"
}

model User {
  id    String @id @default(uuid())
  email String @unique
  name  String?
}

enum Role {
  USER
  ADMIN
}
`
	result := parseAndCompile(t, schema)
	if result.HasErrors() {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Schema == nil {
		t.Fatal("expected non-nil Schema")
	}
	if result.Schema.Datasource == nil {
		t.Error("expected Datasource in IR")
	}
	if len(result.Schema.Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(result.Schema.Models))
	}
	if len(result.Schema.Enums) != 1 {
		t.Errorf("expected 1 enum, got %d", len(result.Schema.Enums))
	}

	// Generate Go code from the compiled IR.
	gen := golang.NewGenerator(result.Schema)
	files, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected generated files")
	}

	var modelsContent string
	for _, f := range files {
		if f.Path == "model/models.go" {
			modelsContent = string(f.Content)
			break
		}
	}
	if modelsContent == "" {
		t.Fatal("model/models.go not found in generated files")
	}
	if !strings.Contains(modelsContent, "type User struct") {
		t.Error("generated code should contain User struct")
	}
}

// ---------------------------------------------------------------------------
// 2. Multi-model schema with relations
// ---------------------------------------------------------------------------

func TestIntegrationMultiModelRelations(t *testing.T) {
	schema := `
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

generator go {
  provider = "gco-go"
  output   = "./gen"
  package  = "db"
}

model User {
  id       String    @id @default(uuid())
  email    String    @unique
  posts    Post[]
  profile  Profile?
}

model Post {
  id       String @id @default(uuid())
  title    String
  authorId String
  author   User   @relation(fields: [authorId], references: [id])
}

model Profile {
  id     String @id @default(uuid())
  bio    String?
  userId String @unique
  user   User   @relation(fields: [userId], references: [id])
}
`
	result := parseAndCompile(t, schema)
	if result.HasErrors() {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if len(result.Schema.Models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(result.Schema.Models))
	}

	modelMap := make(map[string]*ir.Model)
	for _, m := range result.Schema.Models {
		modelMap[m.Name] = m
	}

	// Post must have a relation to User with fields/references populated.
	post := modelMap["Post"]
	if post == nil {
		t.Fatal("Post model not found")
	}
	if len(post.Relations) == 0 {
		t.Fatal("Post should have at least one relation")
	}
	var postUserRel *ir.Relation
	for _, rel := range post.Relations {
		if rel.ToModel == "User" {
			postUserRel = rel
			break
		}
	}
	if postUserRel == nil {
		t.Fatal("Post should have a relation to User")
	}
	if len(postUserRel.Fields) == 0 {
		t.Error("Post→User relation should have local fields")
	}
	if len(postUserRel.References) == 0 {
		t.Error("Post→User relation should have references")
	}

	// Profile must have a relation to User.
	profile := modelMap["Profile"]
	if profile == nil {
		t.Fatal("Profile model not found")
	}
	if len(profile.Relations) == 0 {
		t.Error("Profile should have at least one relation")
	}

	// User must have relations (posts, profile).
	user := modelMap["User"]
	if user == nil {
		t.Fatal("User model not found")
	}
	if len(user.Relations) < 2 {
		t.Errorf("User should have at least 2 relations, got %d", len(user.Relations))
	}

	// Generate and verify all models appear in the output.
	gen := golang.NewGenerator(result.Schema)
	files, err := gen.Generate()
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
	for _, name := range []string{"User", "Post", "Profile"} {
		if !strings.Contains(modelsContent, "type "+name+" struct") {
			t.Errorf("generated code should contain %s struct", name)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Enum integration: enum field type in both IR and generated code
// ---------------------------------------------------------------------------

func TestIntegrationEnumFieldType(t *testing.T) {
	schema := `
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

generator go {
  provider = "gco-go"
  output   = "./gen"
  package  = "db"
}

model User {
  id   String @id @default(uuid())
  role Role   @default(USER)
}

enum Role {
  USER
  ADMIN
  MODERATOR
}
`
	result := parseAndCompile(t, schema)
	if result.HasErrors() {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}

	// Verify enum in IR.
	if len(result.Schema.Enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(result.Schema.Enums))
	}
	roleEnum := result.Schema.Enums[0]
	if roleEnum.Name != "Role" {
		t.Errorf("enum name = %q, want %q", roleEnum.Name, "Role")
	}
	if len(roleEnum.Values) != 3 {
		t.Errorf("expected 3 enum values, got %d", len(roleEnum.Values))
	}

	// Verify the role field is resolved as FieldKindEnum.
	userModel := result.Schema.Models[0]
	var roleField *ir.Field
	for _, f := range userModel.Fields {
		if f.Name == "role" {
			roleField = f
			break
		}
	}
	if roleField == nil {
		t.Fatal("role field not found on User model")
	}
	if roleField.Type != ir.FieldKindEnum {
		t.Errorf("role field kind = %v, want FieldKindEnum", roleField.Type)
	}
	if roleField.EnumType != "Role" {
		t.Errorf("role field enum type = %q, want %q", roleField.EnumType, "Role")
	}

	// Generate and verify enum appears in the generated code.
	gen := golang.NewGenerator(result.Schema)
	files, err := gen.Generate()
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
		t.Fatal("model/enums.go not found in generated files")
	}
	if !strings.Contains(enumsContent, "type Role string") {
		t.Error("generated code should contain Role type")
	}
	if !strings.Contains(enumsContent, "RoleMODERATOR") {
		t.Error("generated code should contain RoleMODERATOR constant")
	}
}

// ---------------------------------------------------------------------------
// 4. Validation errors bubble up
// ---------------------------------------------------------------------------

func TestIntegrationValidationErrors(t *testing.T) {
	// Duplicate model names should be caught at compile time.
	schema := `
model User {
  id String @id
}

model User {
  email String @id
}
`
	result := parseAndCompile(t, schema)
	if !result.HasErrors() {
		t.Fatal("expected validation errors for duplicate model names")
	}

	foundDuplicate := false
	for _, e := range result.Errors {
		if strings.Contains(e.Error(), "duplicate") {
			foundDuplicate = true
			break
		}
	}
	if !foundDuplicate {
		t.Errorf("expected error about duplicate model name, got: %v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// 5. Empty schema
// ---------------------------------------------------------------------------

func TestIntegrationEmptySchema(t *testing.T) {
	result := parseAndCompile(t, "")
	if result.HasErrors() {
		t.Errorf("empty schema should not produce errors: %v", result.Errors)
	}
	if result.Schema == nil {
		t.Fatal("expected non-nil Schema for empty input")
	}
	if len(result.Schema.Models) != 0 {
		t.Errorf("expected 0 models, got %d", len(result.Schema.Models))
	}
	if len(result.Schema.Enums) != 0 {
		t.Errorf("expected 0 enums, got %d", len(result.Schema.Enums))
	}
}
