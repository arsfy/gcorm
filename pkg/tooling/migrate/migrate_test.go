package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsfy/gco-orm/pkg/schema/ir"
)

// testSchemaGCO is a minimal .gco schema used by integration tests.
const testSchemaGCO = `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

model User {
  id    String @id @default(uuid())
  email String @unique
  name  String?
}
`

// writeTestSchema creates a temporary directory with a .gco schema file and
// returns its path. The caller does not need to clean up; t.TempDir handles it.
func writeTestSchema(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "schema")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.gco"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunDiff(t *testing.T) {
	schemaDir := writeTestSchema(t, testSchemaGCO)
	migDir := filepath.Join(t.TempDir(), "migrations")

	err := runDiff([]string{"--name", "init", "--dir", migDir, "--schema", schemaDir})
	if err != nil {
		t.Fatalf("runDiff() error: %v", err)
	}

	// Check migration directory was created.
	entries, err := os.ReadDir(migDir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 migration dir, got %d", len(entries))
	}

	migName := entries[0].Name()
	if !strings.Contains(migName, "init") {
		t.Errorf("migration dir %q doesn't contain 'init'", migName)
	}

	// Check files exist.
	migPath := filepath.Join(migDir, migName)
	for _, f := range []string{"up.sql", "down.sql", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(migPath, f)); err != nil {
			t.Errorf("missing file %s: %v", f, err)
		}
	}

	// Verify manifest JSON.
	data, err := os.ReadFile(filepath.Join(migPath, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var manifest MigrationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	if manifest.Description != "init" {
		t.Errorf("manifest description = %q, want %q", manifest.Description, "init")
	}
	if manifest.Checksum == "" {
		t.Error("manifest checksum is empty")
	}
	if manifest.ToolVersion != "0.1.0" {
		t.Errorf("manifest toolVersion = %q, want %q", manifest.ToolVersion, "0.1.0")
	}
}

func TestRunDiffWithSchema(t *testing.T) {
	schema := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

model User {
  id    String @id @default(uuid())
  email String @unique
  name  String?
}

model Post {
  id    String @id @default(uuid())
  title String
}
`
	schemaDir := writeTestSchema(t, schema)
	migDir := filepath.Join(t.TempDir(), "migrations")

	err := runDiff([]string{"--name", "initial", "--schema", schemaDir, "--dir", migDir})
	if err != nil {
		t.Fatalf("runDiff() error: %v", err)
	}

	entries, err := os.ReadDir(migDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 migration entry, got err=%v entries=%d", err, len(entries))
	}

	migPath := filepath.Join(migDir, entries[0].Name())

	// Verify up.sql contains actual DDL, not stub SQL.
	upSQL, err := os.ReadFile(filepath.Join(migPath, "up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up := string(upSQL)
	if !strings.Contains(up, "CREATE TABLE") {
		t.Error("up.sql should contain CREATE TABLE")
	}
	if strings.Contains(up, "TODO") {
		t.Error("up.sql should not contain stub TODO comment")
	}

	// Verify down.sql has rollback SQL.
	downSQL, err := os.ReadFile(filepath.Join(migPath, "down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(downSQL), "DROP TABLE") {
		t.Error("down.sql should contain DROP TABLE")
	}

	// Verify manifest includes changes and models.
	data, err := os.ReadFile(filepath.Join(migPath, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest MigrationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Changes) == 0 {
		t.Error("manifest should have changes")
	}
	if len(manifest.Models) != 2 {
		t.Errorf("manifest should list 2 models, got %d: %v", len(manifest.Models), manifest.Models)
	}
	// Verify each change has a CreateTable type.
	for _, cr := range manifest.Changes {
		if cr.Type != "CreateTable" {
			t.Errorf("unexpected change type %q for initial migration", cr.Type)
		}
	}
}

func TestRunDiffNoChanges(t *testing.T) {
	// A schema with no models produces zero diff against an empty previous schema.
	schema := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}
`
	schemaDir := writeTestSchema(t, schema)
	migDir := filepath.Join(t.TempDir(), "migrations")

	err := runDiff([]string{"--name", "empty", "--schema", schemaDir, "--dir", migDir})
	if err != nil {
		t.Fatalf("runDiff() error: %v", err)
	}

	// No migration directory should be created.
	if entries, err := os.ReadDir(migDir); err == nil && len(entries) > 0 {
		t.Errorf("expected no migration directories, got %d", len(entries))
	}
}

func TestRunSubcommands(t *testing.T) {
	// runDeploy with no migrations dir should not error.
	if err := runDeploy(nil); err != nil {
		t.Errorf("runDeploy(nil) error: %v", err)
	}
	// runResolve with no args should return a usage error.
	if err := runResolve(nil); err == nil {
		t.Error("runResolve(nil) should return usage error")
	}
}

func TestRunDeployListsMigrations(t *testing.T) {
	dir := t.TempDir()
	migDir := filepath.Join(dir, "migrations")

	// Create two migration directories with manifests.
	for _, name := range []string{"20240101_000000_first", "20240102_000000_second"} {
		p := filepath.Join(migDir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := MigrationManifest{ID: name, ToolVersion: "0.1.0"}
		data, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(p, "manifest.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Should not error; just lists them.
	if err := runDeploy([]string{"--dir", migDir}); err != nil {
		t.Fatalf("runDeploy() error: %v", err)
	}
}

func TestRunResolveValidation(t *testing.T) {
	// No arguments → error.
	if err := runResolve(nil); err == nil {
		t.Error("expected error for missing args")
	}

	// Missing migration_id → error.
	if err := runResolve([]string{"--applied"}); err == nil {
		t.Error("expected error for --applied without migration id")
	}

	// Non-existent migration → error.
	dir := t.TempDir()
	migDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runResolve([]string{"--applied", "nonexistent", "--dir", migDir}); err == nil {
		t.Error("expected error for nonexistent migration")
	}

	// Valid migration → success.
	migName := "20240101_000000_test"
	if err := os.MkdirAll(filepath.Join(migDir, migName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runResolve([]string{"--applied", migName, "--dir", migDir}); err != nil {
		t.Errorf("runResolve() error: %v", err)
	}
}

func TestDetectDialect(t *testing.T) {
	tests := []struct {
		name   string
		schema *ir.Schema
		want   string
	}{
		{"nil schema", nil, "postgresql"},
		{"nil datasource", &ir.Schema{}, "postgresql"},
		{"postgresql", &ir.Schema{Datasource: &ir.Datasource{Provider: "postgresql"}}, "postgresql"},
		{"mysql", &ir.Schema{Datasource: &ir.Datasource{Provider: "mysql"}}, "mysql"},
		{"sqlite", &ir.Schema{Datasource: &ir.Datasource{Provider: "sqlite"}}, "sqlite"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectDialectFromSchema(tt.schema)
			if got != tt.want {
				t.Errorf("detectDialectFromSchema() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunUnknownSubcommand(t *testing.T) {
	err := Run([]string{"unknown"})
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
}

func TestRunNoSubcommand(t *testing.T) {
	err := Run(nil)
	if err == nil {
		t.Fatal("expected error for missing subcommand")
	}
}

func TestChecksumString(t *testing.T) {
	s1 := checksumString("hello")
	s2 := checksumString("hello")
	s3 := checksumString("world")

	if s1 != s2 {
		t.Error("same input should produce same checksum")
	}
	if s1 == s3 {
		t.Error("different input should produce different checksum")
	}
	if len(s1) != 64 { // sha256 hex
		t.Errorf("checksum length = %d, want 64", len(s1))
	}
}

func TestCreateMigrationsTable(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		sql := CreateMigrationsTable(dialect)
		if !strings.Contains(sql, "gco_migrations") {
			t.Errorf("%s: missing table name", dialect)
		}
		if !strings.Contains(sql, "checksum") {
			t.Errorf("%s: missing checksum column", dialect)
		}
		if !strings.Contains(sql, "applied_at") {
			t.Errorf("%s: missing applied_at column", dialect)
		}
	}
}
