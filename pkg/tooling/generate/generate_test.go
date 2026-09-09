package generate

import (
	"os"
	"os/exec"
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

const schemaWithPost = `datasource db {
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
}

model Post {
  id    String @id @default(uuid())
  title String
}
`

// writeSchema creates a schema directory with a .gcorm file and returns its path.
func writeSchema(t *testing.T, dir, schema string) string {
	t.Helper()
	schemaDir := filepath.Join(dir, "schema")
	if err := os.Mkdir(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemaDir, "main.gcorm"), []byte(schema), 0o644); err != nil {
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

// writeGoMod writes a minimal go.mod into dir so that generated import paths
// can be derived from the module path.
func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/gentest\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	writeGoMod(t, dir)
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
		"GUIDE.md",
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

	guideDoc, err := os.ReadFile(filepath.Join(outputDir, "GUIDE.md"))
	if err != nil {
		t.Fatalf("read guide doc: %v", err)
	}
	guideText := string(guideDoc)
	if !strings.Contains(guideText, "# Guide for Generated GCO Client") {
		t.Fatalf("guide doc missing title: %s", guideText)
	}
	if !strings.Contains(guideText, "## About GCORM") || !strings.Contains(guideText, "schema-first ORM") {
		t.Fatalf("guide doc missing project intro: %s", guideText)
	}
	if !strings.Contains(guideText, "https://github.com/arsfy/gcorm/wiki") {
		t.Fatalf("guide doc missing wiki link: %s", guideText)
	}
	if !strings.Contains(guideText, "## gco CLI Basics") || !strings.Contains(guideText, "gco generate") {
		t.Fatalf("guide doc missing gco CLI basics: %s", guideText)
	}
	if !strings.Contains(guideText, "## CRUD Builder Pattern") {
		t.Fatalf("guide doc missing CRUD section: %s", guideText)
	}
	if !strings.Contains(guideText, "query.User.Email") {
		t.Fatalf("guide doc missing model-specific query example: %s", guideText)
	}
	if !strings.Contains(guideText, "## Bulk Inserts") || !strings.Contains(guideText, "OnConflictDoNothing") {
		t.Fatalf("guide doc missing bulk insert docs: %s", guideText)
	}
	if !strings.Contains(guideText, "## Raw SQL") || !strings.Contains(guideText, "client.Raw[model.Post]") {
		t.Fatalf("guide doc missing raw SQL docs: %s", guideText)
	}
	if !strings.Contains(guideText, "## Table and Column Constants") || !strings.Contains(guideText, "query.PostIdColumn") {
		t.Fatalf("guide doc missing table/column constant docs: %s", guideText)
	}
}

// ---------------------------------------------------------------------------
// 2. Output directory creation
// ---------------------------------------------------------------------------

func TestRunOutputDirectoryCreation(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
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

func TestRunRemovesStaleGeneratedFiles(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	chdir(t, dir)
	schemaDir := writeSchema(t, dir, schemaWithPost)

	if err := Run([]string{"--schema", schemaDir}); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	outputDir := filepath.Join(dir, "testout")
	staleGenerated := filepath.Join(outputDir, "query", "post.go")
	if _, err := os.Stat(staleGenerated); err != nil {
		t.Fatalf("expected generated post query before schema change: %v", err)
	}

	manualFile := filepath.Join(outputDir, "query", "manual.go")
	if err := os.WriteFile(manualFile, []byte("package query\n"), 0o644); err != nil {
		t.Fatalf("write manual file: %v", err)
	}

	if err := os.WriteFile(filepath.Join(schemaDir, "main.gcorm"), []byte(validSchema), 0o644); err != nil {
		t.Fatalf("update schema: %v", err)
	}
	if err := Run([]string{"--schema", schemaDir}); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}

	if _, err := os.Stat(staleGenerated); !os.IsNotExist(err) {
		t.Fatalf("stale generated file still exists after schema change")
	}
	if _, err := os.Stat(manualFile); err != nil {
		t.Fatalf("manual file should be preserved: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 3. Deterministic output
// ---------------------------------------------------------------------------

func TestRunDeterministicOutput(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	schemaDir := writeSchema(t, dir, validSchema)

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)

	// First run.
	runDir1 := filepath.Join(dir, "run1")
	if err := os.Mkdir(runDir1, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGoMod(t, runDir1)
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
	writeGoMod(t, runDir2)
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
	if err := os.WriteFile(filepath.Join(dir, "bad.gcorm"), []byte(invalidSchema), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Run([]string{"--schema", dir})
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
}

// ---------------------------------------------------------------------------
// 6. Flag parsing errors
// ---------------------------------------------------------------------------

func TestRunFlagParsingErrors(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"missing schema value", []string{"--schema"}, "requires a value"},
		{"missing config value", []string{"--config"}, "requires a value"},
		{"schema value followed by flag", []string{"--schema", "--dry-run"}, "requires a value"},
		{"empty schema value", []string{"--schema="}, "requires a non-empty value"},
		{"empty separate schema value", []string{"--schema", ""}, "requires a non-empty value"},
		{"empty separate config value", []string{"--config", ""}, "requires a non-empty value"},
		{"unknown flag", []string{"--bogus"}, "unknown flag: --bogus"},
		{"unknown single-dash flag", []string{"-bogus"}, "unknown flag: -bogus"},
		{"dry-run takes no value", []string{"--dry-run=true"}, "does not take a value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Run(tt.args)
			if err == nil {
				t.Fatalf("Run(%v) expected error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// The --flag=value form must keep working.
func TestRunSchemaFlagEqualsForm(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	chdir(t, dir)
	schemaDir := writeSchema(t, dir, validSchema)

	if err := Run([]string{"--schema=" + schemaDir}); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "testout", "model", "models.go")); err != nil {
		t.Errorf("expected generated models.go: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 7. Relative schemaRoots resolve against the config file directory
// ---------------------------------------------------------------------------

func TestRunConfigRelativeSchemaRoots(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	projDir := filepath.Join(dir, "proj")
	if err := os.Mkdir(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSchema(t, projDir, validSchema)

	cfgPath := filepath.Join(projDir, "gco.config.yaml")
	if err := os.WriteFile(cfgPath, []byte("schemaRoots:\n  - schema\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run from an unrelated working directory: the relative schemaRoots entry
	// must resolve against projDir (where the config file lives).
	workDir := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, workDir)

	if err := Run([]string{"--config", cfgPath}); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "testout", "model", "models.go")); err != nil {
		t.Errorf("expected generated models.go: %v", err)
	}
	cmd := exec.Command("go", "test", "./testout/...")
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not compile: %v\n%s", err, out)
	}
}
