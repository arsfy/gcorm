package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSchema = `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

generator go {
  provider = "gco-go"
  output   = "./testout"
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

// writeSchema creates a schema directory with a .gco file and returns its path.
func writeSchema(t *testing.T, dir, schema string) string {
	t.Helper()
	schemaDir := filepath.Join(dir, "schema")
	if err := os.Mkdir(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemaDir, "main.gco"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	return schemaDir
}

// chdir changes to dir and registers a cleanup to restore the original cwd.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

// collectFiles walks root and returns relative-path → content entries.
func collectFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		result[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("collectFiles: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// 1. Generate from schema files
// ---------------------------------------------------------------------------

func TestRunGenerateFromSchemaFiles(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	schemaDir := writeSchema(t, dir, validSchema)

	if err := Run([]string{"--schema", schemaDir}); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// The schema specifies output = "./testout", so files go to cwd/testout.
	outputDir := filepath.Join(dir, "testout")
	expected := []string{
		filepath.Join("model", "models.go"),
		filepath.Join("model", "enums.go"),
		filepath.Join("query", "user.go"),
		filepath.Join("client", "client.go"),
		"AI_USAGE.md",
	}
	for _, rel := range expected {
		path := filepath.Join(outputDir, rel)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", rel)
			continue
		}
		if err != nil {
			t.Errorf("stat %s: %v", rel, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("file %s is empty", rel)
		}
	}

	aiDoc, err := os.ReadFile(filepath.Join(outputDir, "AI_USAGE.md"))
	if err != nil {
		t.Fatalf("read AI usage doc: %v", err)
	}
	aiText := string(aiDoc)
	if !strings.Contains(aiText, "# AI Usage Guide for Generated GCO Client") {
		t.Fatalf("AI usage doc missing title: %s", aiText)
	}
	if !strings.Contains(aiText, "## CRUD Builder Pattern") {
		t.Fatalf("AI usage doc missing CRUD section: %s", aiText)
	}
	if !strings.Contains(aiText, "query.User.Email") {
		t.Fatalf("AI usage doc missing model-specific query example: %s", aiText)
	}
}

// ---------------------------------------------------------------------------
// 2. Output directory creation
// ---------------------------------------------------------------------------

func TestRunOutputDirectoryCreation(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	schemaDir := writeSchema(t, dir, validSchema)

	outputDir := filepath.Join(dir, "testout")
	if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
		t.Fatal("output directory should not exist before Run")
	}

	if err := Run([]string{"--schema", schemaDir}); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	info, err := os.Stat(outputDir)
	if err != nil {
		t.Fatalf("output directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("output path is not a directory")
	}
}

// ---------------------------------------------------------------------------
// 3. Deterministic output
// ---------------------------------------------------------------------------

func TestRunDeterministicOutput(t *testing.T) {
	dir := t.TempDir()
	schemaDir := writeSchema(t, dir, validSchema)

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)

	// First run.
	runDir1 := filepath.Join(dir, "run1")
	if err := os.Mkdir(runDir1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(runDir1); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"--schema", schemaDir}); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	run1Files := collectFiles(t, filepath.Join(runDir1, "testout"))

	// Second run.
	runDir2 := filepath.Join(dir, "run2")
	if err := os.Mkdir(runDir2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(runDir2); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"--schema", schemaDir}); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	run2Files := collectFiles(t, filepath.Join(runDir2, "testout"))

	if len(run1Files) != len(run2Files) {
		t.Fatalf("file count differs: %d vs %d", len(run1Files), len(run2Files))
	}
	for path, content1 := range run1Files {
		content2, ok := run2Files[path]
		if !ok {
			t.Errorf("file %s missing in second run", path)
			continue
		}
		if content1 != content2 {
			t.Errorf("content differs for %s", path)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Missing schema file error
// ---------------------------------------------------------------------------

func TestRunMissingSchemaFile(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	err := Run([]string{"--schema", filepath.Join(dir, "nonexistent")})
	if err == nil {
		t.Fatal("expected error for missing schema path")
	}
}

// ---------------------------------------------------------------------------
// 5. Invalid schema error
// ---------------------------------------------------------------------------

func TestRunInvalidSchema(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	// Missing model name causes a parse error.
	invalidSchema := "model {\n  id String @id\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "bad.gco"), []byte(invalidSchema), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Run([]string{"--schema", dir})
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
}
