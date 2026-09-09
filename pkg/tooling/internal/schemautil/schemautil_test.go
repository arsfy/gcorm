package schemautil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const validSchema = `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

model User {
  id    String @id @default(uuid())
  email String @unique
  name  String?
}
`

const duplicateModelSchema = `model User {
  id String @id
}

model User {
  id String @id
}
`

// writeSchemaDir creates a temp dir containing the given files (name -> content).
func writeSchemaDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gco.config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertHashShape(t *testing.T, hash string) {
	t.Helper()
	if len(hash) != 64 {
		t.Errorf("hash length: got %d, want 64", len(hash))
	}
	for _, c := range hash {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("hash contains non-hex character %q", c)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// LoadFromConfig
// ---------------------------------------------------------------------------

func TestLoadFromConfig(t *testing.T) {
	t.Run("explicit schema path", func(t *testing.T) {
		dir := writeSchemaDir(t, map[string]string{"schema.gcorm": validSchema})
		cfgPath := writeConfig(t, "migrationDir: migrations\n")

		loaded, err := LoadFromConfig(dir, cfgPath)
		if err != nil {
			t.Fatalf("LoadFromConfig() error: %v", err)
		}
		if loaded.Schema == nil {
			t.Fatal("expected non-nil schema")
		}
		if len(loaded.Schema.Models) != 1 || loaded.Schema.Models[0].Name != "User" {
			t.Errorf("expected 1 model named User, got %+v", loaded.Schema.Models)
		}
		assertHashShape(t, loaded.Hash)
	})

	t.Run("schema roots from config", func(t *testing.T) {
		dir := writeSchemaDir(t, map[string]string{"schema.gcorm": validSchema})
		cfgPath := writeConfig(t, "schemaRoots:\n  - "+dir+"\n")

		loaded, err := LoadFromConfig("", cfgPath)
		if err != nil {
			t.Fatalf("LoadFromConfig() error: %v", err)
		}
		if loaded.Schema == nil || len(loaded.Schema.Models) != 1 {
			t.Errorf("expected 1 model, got %+v", loaded.Schema)
		}
	})

	t.Run("missing config file", func(t *testing.T) {
		_, err := LoadFromConfig("", filepath.Join(t.TempDir(), "nope.yaml"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "load config") {
			t.Errorf("error should mention config loading, got: %v", err)
		}
	})

	t.Run("no schema files", func(t *testing.T) {
		dir := writeSchemaDir(t, map[string]string{"README.md": "not a schema"})
		cfgPath := writeConfig(t, "")

		_, err := LoadFromConfig(dir, cfgPath)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no .gcorm schema files found") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		dir := writeSchemaDir(t, map[string]string{"bad.gcorm": "model User {\n"})
		cfgPath := writeConfig(t, "")

		_, err := LoadFromConfig(dir, cfgPath)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "parse error") {
			t.Errorf("error should mention parse error, got: %v", err)
		}
	})

	t.Run("compilation error", func(t *testing.T) {
		dir := writeSchemaDir(t, map[string]string{"dup.gcorm": duplicateModelSchema})
		cfgPath := writeConfig(t, "")

		_, err := LoadFromConfig(dir, cfgPath)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "schema compilation failed") {
			t.Errorf("error should mention compilation failure, got: %v", err)
		}
	})

	t.Run("hash changes with content", func(t *testing.T) {
		cfgPath := writeConfig(t, "")

		dirA := writeSchemaDir(t, map[string]string{"schema.gcorm": validSchema})
		dirB := writeSchemaDir(t, map[string]string{"schema.gcorm": validSchema + "\n"})

		a, err := LoadFromConfig(dirA, cfgPath)
		if err != nil {
			t.Fatalf("LoadFromConfig(dirA) error: %v", err)
		}
		b, err := LoadFromConfig(dirB, cfgPath)
		if err != nil {
			t.Fatalf("LoadFromConfig(dirB) error: %v", err)
		}
		if a.Hash == b.Hash {
			t.Error("hashes should differ when schema contents differ")
		}
	})
}

// ---------------------------------------------------------------------------
// LoadFS
// ---------------------------------------------------------------------------

func TestLoadFS(t *testing.T) {
	t.Run("valid schema", func(t *testing.T) {
		fsys := fstest.MapFS{
			"schema/schema.gcorm": &fstest.MapFile{Data: []byte(validSchema)},
		}
		loaded, err := LoadFS(fsys, "schema")
		if err != nil {
			t.Fatalf("LoadFS() error: %v", err)
		}
		if loaded.Schema == nil || len(loaded.Schema.Models) != 1 {
			t.Errorf("expected 1 model, got %+v", loaded.Schema)
		}
		assertHashShape(t, loaded.Hash)
	})

	t.Run("multiple files merged", func(t *testing.T) {
		fsys := fstest.MapFS{
			"schema/a.gcorm": &fstest.MapFile{Data: []byte("model A {\n  id String @id\n}\n")},
			"schema/b.gcorm": &fstest.MapFile{Data: []byte("model B {\n  id String @id\n}\n")},
		}
		loaded, err := LoadFS(fsys, "schema")
		if err != nil {
			t.Fatalf("LoadFS() error: %v", err)
		}
		if len(loaded.Schema.Models) != 2 {
			t.Errorf("expected 2 models, got %d", len(loaded.Schema.Models))
		}
	})

	t.Run("no schema files", func(t *testing.T) {
		fsys := fstest.MapFS{
			"schema/readme.txt": &fstest.MapFile{Data: []byte("hello")},
		}
		_, err := LoadFS(fsys, "schema")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no .gcorm schema files found in schema") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("no schema files with empty root", func(t *testing.T) {
		fsys := fstest.MapFS{
			"readme.txt": &fstest.MapFile{Data: []byte("hello")},
		}
		_, err := LoadFS(fsys, "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no .gcorm schema files found in .") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		fsys := fstest.MapFS{
			"schema/bad.gcorm": &fstest.MapFile{Data: []byte("model User {\n")},
		}
		_, err := LoadFS(fsys, "schema")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "parse error") {
			t.Errorf("error should mention parse error, got: %v", err)
		}
	})

	t.Run("compilation error", func(t *testing.T) {
		fsys := fstest.MapFS{
			"schema/dup.gcorm": &fstest.MapFile{Data: []byte(duplicateModelSchema)},
		}
		_, err := LoadFS(fsys, "schema")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "schema compilation failed") {
			t.Errorf("error should mention compilation failure, got: %v", err)
		}
	})

	t.Run("missing root", func(t *testing.T) {
		fsys := fstest.MapFS{}
		_, err := LoadFS(fsys, "does-not-exist")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// DiscoverFilesFS
// ---------------------------------------------------------------------------

func TestDiscoverFilesFS(t *testing.T) {
	fsys := fstest.MapFS{
		"schema/a.gcorm":        &fstest.MapFile{Data: []byte(validSchema)},
		"schema/nested/b.gcorm": &fstest.MapFile{Data: []byte(validSchema)},
		"schema/readme.txt":     &fstest.MapFile{Data: []byte("ignored")},
		"other/c.txt":           &fstest.MapFile{Data: []byte("ignored")},
		"single.gcorm":          &fstest.MapFile{Data: []byte(validSchema)},
		"not-a-schema.txt":      &fstest.MapFile{Data: []byte("nope")},
	}

	t.Run("directory walk returns sorted .gcorm files", func(t *testing.T) {
		files, err := DiscoverFilesFS(fsys, "schema")
		if err != nil {
			t.Fatalf("DiscoverFilesFS() error: %v", err)
		}
		want := []string{"schema/a.gcorm", "schema/nested/b.gcorm"}
		if len(files) != len(want) {
			t.Fatalf("got %v, want %v", files, want)
		}
		for i := range want {
			if files[i] != want[i] {
				t.Fatalf("got %v, want %v", files, want)
			}
		}
	})

	t.Run("single .gcorm file", func(t *testing.T) {
		files, err := DiscoverFilesFS(fsys, "single.gcorm")
		if err != nil {
			t.Fatalf("DiscoverFilesFS() error: %v", err)
		}
		if len(files) != 1 || files[0] != "single.gcorm" {
			t.Errorf("got %v, want [single.gcorm]", files)
		}
	})

	t.Run("non-.gcorm file rejected", func(t *testing.T) {
		_, err := DiscoverFilesFS(fsys, "not-a-schema.txt")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "not a .gcorm file") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("root normalization", func(t *testing.T) {
		files, err := DiscoverFilesFS(fsys, " ./schema ")
		if err != nil {
			t.Fatalf("DiscoverFilesFS() error: %v", err)
		}
		if len(files) != 2 {
			t.Errorf("expected 2 files, got %v", files)
		}
	})

	t.Run("empty root walks whole fs", func(t *testing.T) {
		files, err := DiscoverFilesFS(fsys, "")
		if err != nil {
			t.Fatalf("DiscoverFilesFS() error: %v", err)
		}
		if len(files) != 3 {
			t.Errorf("expected 3 files, got %v", files)
		}
	})

	t.Run("missing root", func(t *testing.T) {
		_, err := DiscoverFilesFS(fsys, "does-not-exist")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// HashFiles
// ---------------------------------------------------------------------------

func TestHashFiles(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		files := map[string][]byte{
			"b.gcorm": []byte("model B {\n  id String @id\n}\n"),
			"a.gcorm": []byte("model A {\n  id String @id\n}\n"),
		}
		if HashFiles(files) != HashFiles(files) {
			t.Error("HashFiles should be deterministic")
		}
	})

	t.Run("empty map matches sha256 of empty input", func(t *testing.T) {
		const want = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		if got := HashFiles(map[string][]byte{}); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})

	t.Run("content change changes hash", func(t *testing.T) {
		a := HashFiles(map[string][]byte{"a.gcorm": []byte("x")})
		b := HashFiles(map[string][]byte{"a.gcorm": []byte("y")})
		if a == b {
			t.Error("different contents should produce different hashes")
		}
	})

	t.Run("filename change changes hash", func(t *testing.T) {
		a := HashFiles(map[string][]byte{"a.gcorm": []byte("x")})
		b := HashFiles(map[string][]byte{"b.gcorm": []byte("x")})
		if a == b {
			t.Error("different filenames should produce different hashes")
		}
	})

	t.Run("file order does not matter", func(t *testing.T) {
		// Maps are unordered, so verify via independence from insertion by
		// comparing against the same logical set built differently.
		a := HashFiles(map[string][]byte{
			"a.gcorm": []byte("1"),
			"b.gcorm": []byte("2"),
		})
		b := HashFiles(map[string][]byte{
			"b.gcorm": []byte("2"),
			"a.gcorm": []byte("1"),
		})
		if a != b {
			t.Error("hash should not depend on map iteration order")
		}
	})

	t.Run("output shape", func(t *testing.T) {
		assertHashShape(t, HashFiles(map[string][]byte{"a.gcorm": []byte("x")}))
	})
}
