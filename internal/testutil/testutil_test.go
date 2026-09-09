package testutil

import (
	"errors"
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
	expectFail(t, "AssertEqual mismatch", func(sub *testing.T) {
		AssertEqual(sub, "num", 1, 2)
	})
}

// ---------------------------------------------------------------------------
// 5. AssertNoError and AssertError
// ---------------------------------------------------------------------------

func TestAssertNoError(t *testing.T) {
	// Should not produce any failure for a nil error.
	AssertNoError(t, nil)

	expectFail(t, "AssertNoError with error", func(sub *testing.T) {
		AssertNoError(sub, errSentinel)
	})
}

func TestAssertError(t *testing.T) {
	// Should not produce any failure for a non-nil error.
	AssertError(t, errSentinel)

	expectFail(t, "AssertError with nil", func(sub *testing.T) {
		AssertError(sub, nil)
	})
}

// ---------------------------------------------------------------------------
// 6. Failure paths of file helpers
// ---------------------------------------------------------------------------

func TestReadFileMissing(t *testing.T) {
	dir := TempDir(t)

	expectFail(t, "ReadFile missing file", func(sub *testing.T) {
		ReadFile(sub, filepath.Join(dir, "does-not-exist.txt"))
	})
}

func TestWriteFileInvalidParent(t *testing.T) {
	dir := TempDir(t)

	// Create a regular file and use it as the parent directory so MkdirAll
	// fails ("not a directory").
	filePath := WriteFile(t, dir, "not-a-dir", "")

	expectFail(t, "WriteFile with file as dir", func(sub *testing.T) {
		WriteFile(sub, filePath, "child.txt", "content")
	})
}

func TestWriteFileWriteFails(t *testing.T) {
	dir := TempDir(t)

	// An empty name makes the target path the directory itself, so
	// os.WriteFile fails ("is a directory") after MkdirAll succeeds.
	expectFail(t, "WriteFile to directory path", func(sub *testing.T) {
		WriteFile(sub, dir, "", "content")
	})
}

func TestTempDirFailure(t *testing.T) {
	expectFail(t, "TempDir with invalid TMPDIR", func(sub *testing.T) {
		// Point TMPDIR at a path that cannot hold a temp directory so
		// os.MkdirTemp fails.
		dir := TempDir(t)
		bad := filepath.Join(dir, "missing", "parent")
		sub.Setenv("TMPDIR", bad)
		TempDir(sub)
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var errSentinel = errors.New("sentinel error")

// expectFail runs fn as an internal test and fails the current test unless fn
// caused that internal test to fail. The helpers under test take a
// *testing.T and report via t.Fatal/t.Errorf, so this is the only way to
// exercise their failure paths without failing the real test run.
func expectFail(t *testing.T, name string, fn func(sub *testing.T)) {
	t.Helper()

	ok := testing.RunTests(func(pat, str string) (bool, error) { return true, nil },
		[]testing.InternalTest{
			{Name: name, F: fn},
		},
	)
	if ok {
		t.Errorf("%s: expected helper to fail the test but it did not", name)
	}
}
