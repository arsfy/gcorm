package parser

import (
	"testing"

	"github.com/arsfy/gco-orm/pkg/schema/ast"
)

func TestParseEmptyFile(t *testing.T) {
	doc, err := Parse("test.gcorm", []byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Datasources) != 0 {
		t.Errorf("datasources = %d, want 0", len(doc.Datasources))
	}
	if len(doc.Generators) != 0 {
		t.Errorf("generators = %d, want 0", len(doc.Generators))
	}
	if len(doc.Models) != 0 {
		t.Errorf("models = %d, want 0", len(doc.Models))
	}
	if len(doc.Enums) != 0 {
		t.Errorf("enums = %d, want 0", len(doc.Enums))
	}
}

func TestParseDatasource(t *testing.T) {
	src := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Datasources) != 1 {
		t.Fatalf("datasources = %d, want 1", len(doc.Datasources))
	}
	ds := doc.Datasources[0]
	if ds.Name != "db" {
		t.Errorf("name = %q, want %q", ds.Name, "db")
	}
	if len(ds.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(ds.Entries))
	}

	// provider = "postgresql"
	providerEntry := ds.Entries[0]
	if providerEntry.Key != "provider" {
		t.Errorf("entry[0].Key = %q, want %q", providerEntry.Key, "provider")
	}
	sl, ok := providerEntry.Value.(ast.StringLiteral)
	if !ok {
		t.Fatalf("entry[0].Value type = %T, want StringLiteral", providerEntry.Value)
	}
	if sl.Value != "postgresql" {
		t.Errorf("provider value = %q, want %q", sl.Value, "postgresql")
	}

	// url = env("DATABASE_URL")
	urlEntry := ds.Entries[1]
	if urlEntry.Key != "url" {
		t.Errorf("entry[1].Key = %q, want %q", urlEntry.Key, "url")
	}
	fc, ok := urlEntry.Value.(ast.FunctionCall)
	if !ok {
		t.Fatalf("entry[1].Value type = %T, want FunctionCall", urlEntry.Value)
	}
	if fc.Name != "env" {
		t.Errorf("function name = %q, want %q", fc.Name, "env")
	}
	if len(fc.Args) != 1 {
		t.Fatalf("function args = %d, want 1", len(fc.Args))
	}
	argSL, ok := fc.Args[0].(ast.StringLiteral)
	if !ok {
		t.Fatalf("function arg type = %T, want StringLiteral", fc.Args[0])
	}
	if argSL.Value != "DATABASE_URL" {
		t.Errorf("function arg value = %q, want %q", argSL.Value, "DATABASE_URL")
	}
}

func TestParseGenerator(t *testing.T) {
	src := `generator client {
  provider = "gco-go"
  output   = "./gen"
  package  = "db"
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Generators) != 1 {
		t.Fatalf("generators = %d, want 1", len(doc.Generators))
	}
	gen := doc.Generators[0]
	if gen.Name != "client" {
		t.Errorf("name = %q, want %q", gen.Name, "client")
	}
	if len(gen.Entries) != 3 {
		t.Errorf("entries = %d, want 3", len(gen.Entries))
	}

	expectedKeys := []string{"provider", "output", "package"}
	expectedVals := []string{"gco-go", "./gen", "db"}
	for i, e := range gen.Entries {
		if e.Key != expectedKeys[i] {
			t.Errorf("entry[%d].Key = %q, want %q", i, e.Key, expectedKeys[i])
		}
		sl, ok := e.Value.(ast.StringLiteral)
		if !ok {
			t.Errorf("entry[%d].Value type = %T, want StringLiteral", i, e.Value)
			continue
		}
		if sl.Value != expectedVals[i] {
			t.Errorf("entry[%d].Value = %q, want %q", i, sl.Value, expectedVals[i])
		}
	}
}

func TestParseSimpleModel(t *testing.T) {
	src := `model User {
  id    String
  email String
  age   Int
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(doc.Models))
	}
	m := doc.Models[0]
	if m.Name != "User" {
		t.Errorf("name = %q, want %q", m.Name, "User")
	}
	if len(m.Fields) != 3 {
		t.Fatalf("fields = %d, want 3", len(m.Fields))
	}

	expectedNames := []string{"id", "email", "age"}
	expectedTypes := []string{"String", "String", "Int"}
	for i, f := range m.Fields {
		if f.Name != expectedNames[i] {
			t.Errorf("field[%d].Name = %q, want %q", i, f.Name, expectedNames[i])
		}
		if f.Type.Name != expectedTypes[i] {
			t.Errorf("field[%d].Type.Name = %q, want %q", i, f.Type.Name, expectedTypes[i])
		}
	}
}

func TestParseModelWithAttributes(t *testing.T) {
	src := `model User {
  id    String @id @default(uuid())
  email String @unique
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(doc.Models))
	}
	m := doc.Models[0]
	if len(m.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(m.Fields))
	}

	// Field "id" has @id and @default(uuid())
	idField := m.Fields[0]
	if idField.Name != "id" {
		t.Errorf("field[0].Name = %q, want %q", idField.Name, "id")
	}
	if len(idField.Attributes) != 2 {
		t.Fatalf("field id attrs = %d, want 2", len(idField.Attributes))
	}
	if idField.Attributes[0].Name != "id" {
		t.Errorf("attr[0].Name = %q, want %q", idField.Attributes[0].Name, "id")
	}
	if idField.Attributes[1].Name != "default" {
		t.Errorf("attr[1].Name = %q, want %q", idField.Attributes[1].Name, "default")
	}
	// @default has a FunctionCall arg "uuid"
	if len(idField.Attributes[1].Args) != 1 {
		t.Fatalf("@default args = %d, want 1", len(idField.Attributes[1].Args))
	}
	fc, ok := idField.Attributes[1].Args[0].Value.(ast.FunctionCall)
	if !ok {
		t.Fatalf("@default arg type = %T, want FunctionCall", idField.Attributes[1].Args[0].Value)
	}
	if fc.Name != "uuid" {
		t.Errorf("@default function name = %q, want %q", fc.Name, "uuid")
	}

	// Field "email" has @unique
	emailField := m.Fields[1]
	if emailField.Name != "email" {
		t.Errorf("field[1].Name = %q, want %q", emailField.Name, "email")
	}
	if len(emailField.Attributes) != 1 {
		t.Fatalf("field email attrs = %d, want 1", len(emailField.Attributes))
	}
	if emailField.Attributes[0].Name != "unique" {
		t.Errorf("email attr[0].Name = %q, want %q", emailField.Attributes[0].Name, "unique")
	}
}

func TestParseOptionalAndListFields(t *testing.T) {
	src := `model Post {
  name    String?
  tags    String[]
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := doc.Models[0]
	if len(m.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(m.Fields))
	}

	nameField := m.Fields[0]
	if nameField.Name != "name" {
		t.Errorf("field[0].Name = %q, want %q", nameField.Name, "name")
	}
	if !nameField.Type.IsOptional {
		t.Error("name field should be optional")
	}
	if nameField.Type.IsList {
		t.Error("name field should not be a list")
	}

	tagsField := m.Fields[1]
	if tagsField.Name != "tags" {
		t.Errorf("field[1].Name = %q, want %q", tagsField.Name, "tags")
	}
	if !tagsField.Type.IsList {
		t.Error("tags field should be a list")
	}
	if tagsField.Type.IsOptional {
		t.Error("tags field should not be optional")
	}
}

func TestParseModelLevelAttributes(t *testing.T) {
	src := `model User {
  id        String
  createdAt DateTime

  @@index([createdAt])
  @@map("users")
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := doc.Models[0]
	if len(m.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(m.Fields))
	}
	if len(m.Attributes) != 2 {
		t.Fatalf("model attrs = %d, want 2", len(m.Attributes))
	}

	// @@index([createdAt])
	indexAttr := m.Attributes[0]
	if indexAttr.Name != "index" {
		t.Errorf("attr[0].Name = %q, want %q", indexAttr.Name, "index")
	}
	if !indexAttr.IsModelLevel {
		t.Error("@@index should be model-level")
	}
	if len(indexAttr.Args) != 1 {
		t.Fatalf("@@index args = %d, want 1", len(indexAttr.Args))
	}
	_, ok := indexAttr.Args[0].Value.(ast.ArrayLiteral)
	if !ok {
		t.Errorf("@@index arg type = %T, want ArrayLiteral", indexAttr.Args[0].Value)
	}

	// @@map("users")
	mapAttr := m.Attributes[1]
	if mapAttr.Name != "map" {
		t.Errorf("attr[1].Name = %q, want %q", mapAttr.Name, "map")
	}
	if !mapAttr.IsModelLevel {
		t.Error("@@map should be model-level")
	}
	if len(mapAttr.Args) != 1 {
		t.Fatalf("@@map args = %d, want 1", len(mapAttr.Args))
	}
	sl, ok := mapAttr.Args[0].Value.(ast.StringLiteral)
	if !ok {
		t.Errorf("@@map arg type = %T, want StringLiteral", mapAttr.Args[0].Value)
	}
	if sl.Value != "users" {
		t.Errorf("@@map value = %q, want %q", sl.Value, "users")
	}
}

func TestParseEnum(t *testing.T) {
	src := `enum Role {
  USER
  ADMIN
  MODERATOR
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Enums) != 1 {
		t.Fatalf("enums = %d, want 1", len(doc.Enums))
	}
	e := doc.Enums[0]
	if e.Name != "Role" {
		t.Errorf("name = %q, want %q", e.Name, "Role")
	}
	if len(e.Values) != 3 {
		t.Fatalf("values = %d, want 3", len(e.Values))
	}

	expectedValues := []string{"USER", "ADMIN", "MODERATOR"}
	for i, v := range e.Values {
		if v.Name != expectedValues[i] {
			t.Errorf("value[%d] = %q, want %q", i, v.Name, expectedValues[i])
		}
	}
}

func TestParseRelationAttribute(t *testing.T) {
	src := `model Post {
  authorId String
  author   User   @relation(fields: [authorId], references: [id])
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := doc.Models[0]
	if len(m.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(m.Fields))
	}

	authorField := m.Fields[1]
	if authorField.Name != "author" {
		t.Errorf("field[1].Name = %q, want %q", authorField.Name, "author")
	}
	if authorField.Type.Name != "User" {
		t.Errorf("field type = %q, want %q", authorField.Type.Name, "User")
	}
	if len(authorField.Attributes) != 1 {
		t.Fatalf("author attrs = %d, want 1", len(authorField.Attributes))
	}

	relAttr := authorField.Attributes[0]
	if relAttr.Name != "relation" {
		t.Errorf("attr name = %q, want %q", relAttr.Name, "relation")
	}
	if len(relAttr.Args) != 2 {
		t.Fatalf("@relation args = %d, want 2", len(relAttr.Args))
	}

	// Named arg: fields
	fieldsArg := relAttr.Args[0]
	if fieldsArg.Name != "fields" {
		t.Errorf("arg[0].Name = %q, want %q", fieldsArg.Name, "fields")
	}
	arr, ok := fieldsArg.Value.(ast.ArrayLiteral)
	if !ok {
		t.Fatalf("fields arg type = %T, want ArrayLiteral", fieldsArg.Value)
	}
	if len(arr.Elements) != 1 {
		t.Fatalf("fields array len = %d, want 1", len(arr.Elements))
	}

	// Named arg: references
	refsArg := relAttr.Args[1]
	if refsArg.Name != "references" {
		t.Errorf("arg[1].Name = %q, want %q", refsArg.Name, "references")
	}
	arr2, ok := refsArg.Value.(ast.ArrayLiteral)
	if !ok {
		t.Fatalf("references arg type = %T, want ArrayLiteral", refsArg.Value)
	}
	if len(arr2.Elements) != 1 {
		t.Fatalf("references array len = %d, want 1", len(arr2.Elements))
	}
}

func TestParseMultiBlockFile(t *testing.T) {
	src := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

model User {
  id   String
  name String
}

enum Role {
  USER
  ADMIN
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Datasources) != 1 {
		t.Errorf("datasources = %d, want 1", len(doc.Datasources))
	}
	if len(doc.Models) != 1 {
		t.Errorf("models = %d, want 1", len(doc.Models))
	}
	if len(doc.Enums) != 1 {
		t.Errorf("enums = %d, want 1", len(doc.Enums))
	}
}

func TestParseFullSchema(t *testing.T) {
	src := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

model User {
  id        String   @id @default(uuid())
  email     String   @unique
  name      String?
  posts     Post[]
  createdAt DateTime @default(now())
  @@map("users")
}

model Post {
  id       String  @id @default(uuid())
  title    String
  content  String?
  authorId String
  author   User    @relation(fields: [authorId], references: [id])
  @@index([authorId])
}

enum Role {
  USER
  ADMIN
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(doc.Datasources) != 1 {
		t.Errorf("datasources = %d, want 1", len(doc.Datasources))
	}
	if len(doc.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(doc.Models))
	}
	if len(doc.Enums) != 1 {
		t.Errorf("enums = %d, want 1", len(doc.Enums))
	}

	// User model
	user := doc.Models[0]
	if user.Name != "User" {
		t.Errorf("model[0].Name = %q, want %q", user.Name, "User")
	}
	if len(user.Fields) != 5 {
		t.Errorf("User fields = %d, want 5", len(user.Fields))
	}
	if len(user.Attributes) != 1 {
		t.Errorf("User model attrs = %d, want 1", len(user.Attributes))
	}

	// Post model
	post := doc.Models[1]
	if post.Name != "Post" {
		t.Errorf("model[1].Name = %q, want %q", post.Name, "Post")
	}
	if len(post.Fields) != 5 {
		t.Errorf("Post fields = %d, want 5", len(post.Fields))
	}
	if len(post.Attributes) != 1 {
		t.Errorf("Post model attrs = %d, want 1", len(post.Attributes))
	}

	// Enum
	role := doc.Enums[0]
	if role.Name != "Role" {
		t.Errorf("enum name = %q, want %q", role.Name, "Role")
	}
	if len(role.Values) != 2 {
		t.Errorf("enum values = %d, want 2", len(role.Values))
	}
}

func TestParseMulti(t *testing.T) {
	files := map[string][]byte{
		"a.gcorm": []byte(`model User {
  id String
}`),
		"b.gcorm": []byte(`model Post {
  id String
}`),
	}
	ds, err := ParseMulti(files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ds.Documents) != 2 {
		t.Fatalf("documents = %d, want 2", len(ds.Documents))
	}
	if len(ds.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(ds.Files))
	}
	// Files should be sorted alphabetically.
	if ds.Files[0] != "a.gcorm" {
		t.Errorf("file[0] = %q, want %q", ds.Files[0], "a.gcorm")
	}
	if ds.Files[1] != "b.gcorm" {
		t.Errorf("file[1] = %q, want %q", ds.Files[1], "b.gcorm")
	}
	if len(ds.Documents[0].Models) != 1 {
		t.Errorf("doc[0] models = %d, want 1", len(ds.Documents[0].Models))
	}
	if len(ds.Documents[1].Models) != 1 {
		t.Errorf("doc[1] models = %d, want 1", len(ds.Documents[1].Models))
	}
}

func TestParseComments(t *testing.T) {
	src := `// This is a comment
/// This is a doc comment
model User {
  id String
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(doc.Models))
	}

	// Comments are attached to the model declaration.
	m := doc.Models[0]
	if len(m.Comments) < 2 {
		t.Fatalf("model comments = %d, want at least 2", len(m.Comments))
	}

	foundLine := false
	foundDoc := false
	for _, c := range m.Comments {
		if c.Text == "This is a comment" && !c.IsDoc {
			foundLine = true
		}
		if c.Text == "This is a doc comment" && c.IsDoc {
			foundDoc = true
		}
	}
	if !foundLine {
		t.Error("expected line comment 'This is a comment' on model")
	}
	if !foundDoc {
		t.Error("expected doc comment 'This is a doc comment' on model")
	}
}

func TestParseErrorCases(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"missing model name", "model {"},
		{"unknown keyword", "foobar {}"},
		{"unterminated block", "model User {"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("test.gcorm", []byte(tt.src))
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestParseBooleanLiteral(t *testing.T) {
	src := `generator client {
  provider    = "gco-go"
  emitRuntime = false
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Generators) != 1 {
		t.Fatalf("generators = %d, want 1", len(doc.Generators))
	}
	gen := doc.Generators[0]
	if len(gen.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(gen.Entries))
	}

	runtimeEntry := gen.Entries[1]
	if runtimeEntry.Key != "emitRuntime" {
		t.Errorf("key = %q, want %q", runtimeEntry.Key, "emitRuntime")
	}
	bl, ok := runtimeEntry.Value.(ast.BooleanLiteral)
	if !ok {
		t.Fatalf("value type = %T, want BooleanLiteral", runtimeEntry.Value)
	}
	if bl.Value != false {
		t.Errorf("boolean value = %v, want false", bl.Value)
	}
}

func TestParseNativeTypeAttribute(t *testing.T) {
	src := `model User {
  name String @db.VarChar(255)
}`
	doc, err := Parse("test.gcorm", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := doc.Models[0]
	if len(m.Fields) != 1 {
		t.Fatalf("fields = %d, want 1", len(m.Fields))
	}
	f := m.Fields[0]
	if len(f.Attributes) != 1 {
		t.Fatalf("attrs = %d, want 1", len(f.Attributes))
	}
	attr := f.Attributes[0]
	if attr.Name != "db.VarChar" {
		t.Errorf("attr name = %q, want %q", attr.Name, "db.VarChar")
	}
	if len(attr.Args) != 1 {
		t.Fatalf("attr args = %d, want 1", len(attr.Args))
	}
	nl, ok := attr.Args[0].Value.(ast.NumberLiteral)
	if !ok {
		t.Fatalf("arg type = %T, want NumberLiteral", attr.Args[0].Value)
	}
	if nl.Value != "255" {
		t.Errorf("number value = %q, want %q", nl.Value, "255")
	}
}
