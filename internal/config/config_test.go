package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MigrationDir != "migrations" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "migrations")
	}
	if cfg.Format == nil {
		t.Fatal("Format is nil")
	}
	if cfg.Format.IndentWidth != 2 {
		t.Errorf("IndentWidth = %d, want 2", cfg.Format.IndentWidth)
	}
}

func TestLoadExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.yaml")
	content := `schemaRoots:
  - ./schema
migrationDir: db/migrations
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if path != cfgPath {
		t.Errorf("config path = %q, want %q", path, cfgPath)
	}
	if len(cfg.SchemaRoots) != 1 || cfg.SchemaRoots[0] != "./schema" {
		t.Errorf("SchemaRoots = %v, want [./schema]", cfg.SchemaRoots)
	}
	if cfg.MigrationDir != "db/migrations" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "db/migrations")
	}
}

func TestLoadAutoDiscover(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gco.config.yaml")
	content := `migrationDir: migs`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	cfg, _, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MigrationDir != "migs" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "migs")
	}
}

func TestLoadDefaultWhenNoConfig(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if path != "" {
		t.Errorf("expected empty path, got %q", path)
	}
	if cfg.MigrationDir != "migrations" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "migrations")
	}
}

func TestDiscoverSchemaRootsFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{SchemaRoots: []string{"schema", "prisma"}}
	roots, err := DiscoverSchemaRoots(cfg, dir)
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots count = %d, want 2", len(roots))
	}
}

func TestDiscoverSchemaRootsAutoDiscover(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "schema"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	roots, err := DiscoverSchemaRoots(cfg, dir)
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("roots count = %d, want 1", len(roots))
	}
	if filepath.Base(roots[0]) != "schema" {
		t.Errorf("root = %q, want schema dir", roots[0])
	}
}

func TestDiscoverSchemaRootsConflict(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "schema"), 0o755)
	os.Mkdir(filepath.Join(dir, "prisma"), 0o755)

	cfg := &Config{}
	_, err := DiscoverSchemaRoots(cfg, dir)
	if err == nil {
		t.Fatal("expected error for conflicting schema dirs")
	}
}

func TestDiscoverSchemaFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "models.gco"), []byte("model X {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "enums.gco"), []byte("enum Y { A }"), 0o644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hi"), 0o644)

	files, err := DiscoverSchemaFiles([]string{dir})
	if err != nil {
		t.Fatalf("DiscoverSchemaFiles() error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files count = %d, want 2", len(files))
	}
}

func TestDiscoverSchemaFilesExcludesVendor(t *testing.T) {
	dir := t.TempDir()
	vendorDir := filepath.Join(dir, "vendor")
	os.Mkdir(vendorDir, 0o755)
	os.WriteFile(filepath.Join(vendorDir, "skip.gco"), []byte("model X {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "keep.gco"), []byte("model Y {}"), 0o644)

	files, err := DiscoverSchemaFiles([]string{dir})
	if err != nil {
		t.Fatalf("DiscoverSchemaFiles() error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files count = %d, want 1 (vendor should be excluded)", len(files))
	}
}
