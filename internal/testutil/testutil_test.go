package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// 1. TempDir creates and cleans up directory
// ---------------------------------------------------------------------------

func TestTempDir(t *testing.T) {
	var dir string

	// Run in a sub-test so Cleanup fires before we check removal.
	t.Run("create", func(t *testing.T) {
		dir = TempDir(t)
		if dir == "" {
			t.Fatal("TempDir returned empty string")
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("TempDir directory does not exist: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("TempDir path is not a directory: %s", dir)
		}
	})

	// After the sub-test completes, Cleanup should have removed the dir.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("TempDir cleanup did not remove directory: %s (err=%v)", dir, err)
		// Best-effort cleanup.
		os.RemoveAll(dir)
	}
}

// ---------------------------------------------------------------------------
// 2. WriteFile creates file with content
// ---------------------------------------------------------------------------

func TestWriteFile(t *testing.T) {
	dir := TempDir(t)

	path := WriteFile(t, dir, "hello.txt", "world")
	if path == "" {
		t.Fatal("WriteFile returned empty path")
	}

	expectedPath := filepath.Join(dir, "hello.txt")
	if path != expectedPath {
		t.Errorf("WriteFile path = %q, want %q", path, expectedPath)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read written file: %v", err)
	}
	if string(data) != "world" {
		t.Errorf("file content = %q, want %q", string(data), "world")
	}
}

func TestWriteFileNestedDir(t *testing.T) {
	dir := TempDir(t)

	path := WriteFile(t, dir, "sub/dir/file.txt", "nested content")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read nested file: %v", err)
	}
	if string(data) != "nested content" {
		t.Errorf("file content = %q, want %q", string(data), "nested content")
	}
}

// ---------------------------------------------------------------------------
// 3. ReadFile reads back written content
// ---------------------------------------------------------------------------

func TestReadFile(t *testing.T) {
	dir := TempDir(t)

	content := "the quick brown fox"
	path := WriteFile(t, dir, "fox.txt", content)
	got := ReadFile(t, path)
	if got != content {
		t.Errorf("ReadFile = %q, want %q", got, content)
	}
}

func TestReadFileEmpty(t *testing.T) {
	dir := TempDir(t)

	path := WriteFile(t, dir, "empty.txt", "")
	got := ReadFile(t, path)
	if got != "" {
		t.Errorf("ReadFile of empty file = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// 4. AssertEqual works for matching and catching mismatches
// ---------------------------------------------------------------------------

func TestAssertEqualMatch(t *testing.T) {
	// Should not produce any errors when values match.
	AssertEqual(t, "int", 42, 42)
	AssertEqual(t, "string", "hello", "hello")
	AssertEqual(t, "bool", true, true)
}

func TestAssertEqualMismatch(t *testing.T) {
	// We cannot pass a fake to AssertEqual (it requires *testing.T), so we
	// verify indirectly: run a sub-test that is expected to report a failure.
	result := testing.RunTests(func(pat, str string) (bool, error) { return true, nil },
		[]testing.InternalTest{
			{
				Name: "Mismatch",
				F: func(sub *testing.T) {
					AssertEqual(sub, "num", 1, 2)
				},
			},
		},
	)
	// result == false means at least one test failed, which is the expected
	// behaviour when AssertEqual detects a mismatch.
	if result {
		t.Error("AssertEqual(1, 2) should have reported an error but did not")
	}
}
