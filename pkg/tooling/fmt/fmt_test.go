package gcofmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 1. Format a single file
// ---------------------------------------------------------------------------

func TestFormatSingleFile(t *testing.T) {
	dir := t.TempDir()

	unformatted := "model   User   {\nid    String   @id    @default(uuid())\n  email     String   @unique\n}\n"
	path := filepath.Join(dir, "test.gcorm")
	if err := os.WriteFile(path, []byte(unformatted), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run([]string{path}); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	formatted := string(data)

	if formatted == unformatted {
		t.Error("file content should have changed after formatting")
	}
	if !strings.Contains(formatted, "model User {") {
		t.Error("expected canonical model declaration (single spaces)")
	}
	if !strings.Contains(formatted, "\n  id") {
		t.Error("expected 2-space indentation for fields")
	}
}

// ---------------------------------------------------------------------------
// 2. Format is idempotent
// ---------------------------------------------------------------------------

func TestFormatIdempotent(t *testing.T) {
	dir := t.TempDir()

	// Already canonical: the formatter should produce identical output.
	schema := "model User {\n  id    String @id\n  email String @unique\n}\n"
	path := filepath.Join(dir, "test.gcorm")
	if err := os.WriteFile(path, []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}

	// First format.
	if err := Run([]string{path}); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	data1, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Second format.
	if err := Run([]string{path}); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(data1) != string(data2) {
		t.Error("formatting should be idempotent; content changed on second run")
	}
}

// ---------------------------------------------------------------------------
// 3. Format non-existent file
// ---------------------------------------------------------------------------

func TestFormatNonExistentFile(t *testing.T) {
	err := Run([]string{"/nonexistent/path/schema.gcorm"})
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

// ---------------------------------------------------------------------------
// 4. Format multiple files in directory
// ---------------------------------------------------------------------------

func TestFormatMultipleFilesInDirectory(t *testing.T) {
	dir := t.TempDir()

	file1 := "model  Alpha {\nid  String  @id\n}\n"
	file2 := "model  Beta {\nid  String @id\n}\n"

	if err := os.WriteFile(filepath.Join(dir, "alpha.gcorm"), []byte(file1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.gcorm"), []byte(file2), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run([]string{dir}); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	for _, name := range []string{"alpha.gcorm", "beta.gcorm"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, "\n  id") {
			t.Errorf("file %s should have 2-space indentation after formatting", name)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. Check mode (--check)
// ---------------------------------------------------------------------------

func TestFormatCheckMode(t *testing.T) {
	dir := t.TempDir()

	unformatted := "model   User    {\nid    String   @id\n}\n"
	path := filepath.Join(dir, "test.gcorm")
	if err := os.WriteFile(path, []byte(unformatted), 0o644); err != nil {
		t.Fatal(err)
	}

	// --check on an unformatted file should return an error.
	err := Run([]string{"--check", path})
	if err == nil {
		t.Fatal("expected error from --check on unformatted file")
	}

	// Verify the file was NOT modified.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != unformatted {
		t.Error("--check should not modify the file")
	}

	// Now actually format the file.
	if err := Run([]string{path}); err != nil {
		t.Fatalf("format Run() error: %v", err)
	}

	// --check on the formatted file should succeed.
	if err := Run([]string{"--check", path}); err != nil {
		t.Errorf("--check should pass on formatted file: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 6. Unchanged file is not rewritten
// ---------------------------------------------------------------------------

func TestFormatSkipsUnchangedFile(t *testing.T) {
	dir := t.TempDir()

	// Already canonical: the formatter produces identical output.
	schema := "model User {\n  id    String @id\n  email String @unique\n}\n"
	path := filepath.Join(dir, "test.gcorm")
	if err := os.WriteFile(path, []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pin the modification time in the past so a rewrite is detectable.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if err := Run([]string{path}); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("mtime changed for already-formatted file: got %v, want %v", info.ModTime(), old)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != schema {
		t.Error("content changed for already-formatted file")
	}
}
