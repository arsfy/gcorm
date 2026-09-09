package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildGCO compiles the gco binary into a temporary directory.
func buildGCO(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gco")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build gco: %v\n%s", err, out)
	}
	return bin
}

type gcoResult struct {
	exitCode int
	stdout   string
	stderr   string
}

// runGCO executes the gco binary with args in dir and captures its exit code
// and output.
func runGCO(t *testing.T, bin, dir string, args ...string) gcoResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	res := gcoResult{}
	err := cmd.Run()
	res.stdout = stdout.String()
	res.stderr = stderr.String()
	if err == nil {
		return res
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.exitCode = exitErr.ExitCode()
		return res
	}
	t.Fatalf("run gco %v: %v", args, err)
	return res
}

const smokeSchema = `datasource db {
  provider = "sqlite"
  url      = env("DATABASE_URL")
}

generator client {
  provider = "gco-go"
  output   = "./gen"
  package  = "db"
}

model User {
  id    Int     @id @default(autoincrement())
  email String  @unique
  name  String?
}
`

func TestSmokeCLI(t *testing.T) {
	bin := buildGCO(t)

	simpleSchema, err := os.ReadFile(filepath.Join("..", "..", "testdata", "simple.gcorm"))
	if err != nil {
		t.Fatalf("read testdata/simple.gcorm: %v", err)
	}

	tests := []struct {
		name       string
		args       []string
		files      map[string]string // files created in the working dir before running
		workDir    string            // subdirectory of the working dir to run in
		wantExit   int
		wantStdout string // substring expected on stdout
		wantStderr string // substring expected on stderr
		wantFiles  []string
		wantAbsent []string
	}{
		{
			name:       "no args prints usage",
			args:       nil,
			wantExit:   1,
			wantStdout: "Usage:",
		},
		{
			name:       "help prints usage",
			args:       []string{"help"},
			wantExit:   0,
			wantStdout: "Usage:",
		},
		{
			name:       "version prints version",
			args:       []string{"version"},
			wantExit:   0,
			wantStdout: "gco ",
		},
		{
			name:       "unknown command fails",
			args:       []string{"frobnicate"},
			wantExit:   1,
			wantStderr: "unknown command: frobnicate",
		},
		{
			name:       "unknown db subcommand fails",
			args:       []string{"db"},
			wantExit:   1,
			wantStderr: "unknown db subcommand",
		},
		{
			name:       "init sqlite non-interactive",
			args:       []string{"init", "--yes", "--provider", "sqlite"},
			wantExit:   0,
			wantStdout: "Initialized schema at",
			wantFiles:  []string{filepath.Join("schema", "schema.gcorm")},
		},
		{
			name:       "init refuses to overwrite without force",
			args:       []string{"init", "--yes", "--provider", "sqlite"},
			files:      map[string]string{filepath.Join("schema", "schema.gcorm"): string(simpleSchema)},
			wantExit:   1,
			wantStderr: "already exists",
		},
		{
			name:       "init rejects unsupported provider",
			args:       []string{"init", "--yes", "--provider", "oracle"},
			wantExit:   1,
			wantStderr: "unsupported provider",
		},
		{
			name:       "validate valid schema",
			args:       []string{"validate", "--schema", "schema.gcorm"},
			files:      map[string]string{"schema.gcorm": string(simpleSchema)},
			wantExit:   0,
			wantStdout: "Validated 1 schema file(s) successfully.",
		},
		{
			name:       "validate invalid schema fails",
			args:       []string{"validate", "--schema", "schema.gcorm"},
			files:      map[string]string{"schema.gcorm": "model {\n"},
			wantExit:   1,
			wantStderr: "error:",
		},
		{
			name:       "validate without schema files fails",
			args:       []string{"validate"},
			wantExit:   1,
			wantStderr: "no .gcorm schema files",
		},
		{
			name:     "generate writes client code and guide",
			args:     []string{"generate"},
			files:    map[string]string{"go.mod": "module example.com/smoke\n", filepath.Join("schema", "schema.gcorm"): smokeSchema},
			wantExit: 0,
			wantFiles: []string{
				filepath.Join("gen", "model", "models.go"),
				filepath.Join("gen", "query", "user.go"),
				filepath.Join("gen", "client", "client.go"),
				filepath.Join("gen", "GUIDE.md"),
			},
		},
		{
			name:       "generate dry run writes nothing",
			args:       []string{"generate", "--dry-run"},
			files:      map[string]string{"go.mod": "module example.com/smoke\n", filepath.Join("schema", "schema.gcorm"): smokeSchema},
			wantExit:   0,
			wantStdout: "Dry run:",
			wantAbsent: []string{"gen"},
		},
		{
			name:       "generate invalid schema fails",
			args:       []string{"generate"},
			files:      map[string]string{filepath.Join("schema", "schema.gcorm"): "model {\n"},
			wantExit:   1,
			wantStderr: "error:",
		},
		{
			name:       "validate missing schema flag value fails",
			args:       []string{"validate", "--schema"},
			wantExit:   1,
			wantStderr: "requires a value",
		},
		{
			name:       "validate flag value followed by flag fails",
			args:       []string{"validate", "--schema", "--config", "x.yaml"},
			wantExit:   1,
			wantStderr: "requires a value",
		},
		{
			name:       "validate empty schema flag value fails",
			args:       []string{"validate", "--schema", ""},
			wantExit:   1,
			wantStderr: "requires a non-empty value",
		},
		{
			name:       "validate empty config flag value fails",
			args:       []string{"validate", "--config", ""},
			wantExit:   1,
			wantStderr: "requires a non-empty value",
		},
		{
			name:       "validate unknown flag fails",
			args:       []string{"validate", "--bogus"},
			wantExit:   1,
			wantStderr: "unknown flag: --bogus",
		},
		{
			name:       "generate missing config flag value fails",
			args:       []string{"generate", "--config"},
			wantExit:   1,
			wantStderr: "requires a value",
		},
		{
			name:       "generate unknown flag fails",
			args:       []string{"generate", "--bogus"},
			wantExit:   1,
			wantStderr: "unknown flag: --bogus",
		},
		{
			name:    "generate resolves schemaRoots relative to config file",
			args:    []string{"generate"},
			workDir: "sub",
			files: map[string]string{
				"gco.config.yaml":                       "schemaRoots:\n  - schema\n",
				"go.mod":                                "module example.com/smoke\n",
				filepath.Join("schema", "schema.gcorm"): smokeSchema,
				filepath.Join("sub", ".keep"):           "",
			},
			wantExit: 0,
			wantFiles: []string{
				filepath.Join("sub", "gen", "model", "models.go"),
				filepath.Join("sub", "gen", "client", "client.go"),
			},
		},
		{
			name:       "fmt check fails on unformatted file",
			args:       []string{"fmt", "--check", "schema.gcorm"},
			files:      map[string]string{"schema.gcorm": "model User {\n  id String @id\n    email String @unique\n}\n"},
			wantExit:   1,
			wantStderr: "not formatted",
		},
		{
			name: "fmt check passes on formatted file",
			args: []string{"fmt", "--check", "schema.gcorm"},
			files: map[string]string{"schema.gcorm": `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

model User {
  id    String  @id @default(uuid())
  email String  @unique
  name  String?
}
`},
			wantExit: 0,
		},
		{
			name:       "fmt without files fails",
			args:       []string{"fmt"},
			wantExit:   1,
			wantStderr: "no .gcorm files found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			runDir := dir
			if tt.workDir != "" {
				runDir = filepath.Join(dir, tt.workDir)
			}

			res := runGCO(t, bin, runDir, tt.args...)

			if res.exitCode != tt.wantExit {
				t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s",
					res.exitCode, tt.wantExit, res.stdout, res.stderr)
			}
			if tt.wantStdout != "" && !strings.Contains(res.stdout, tt.wantStdout) {
				t.Errorf("stdout missing %q\ngot: %s", tt.wantStdout, res.stdout)
			}
			if tt.wantStderr != "" && !strings.Contains(res.stderr, tt.wantStderr) {
				t.Errorf("stderr missing %q\ngot: %s", tt.wantStderr, res.stderr)
			}
			for _, f := range tt.wantFiles {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Errorf("expected file %s to exist: %v", f, err)
				}
			}
			for _, f := range tt.wantAbsent {
				if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
					t.Errorf("expected %s to be absent, stat err = %v", f, err)
				}
			}
		})
	}
}

// TestSmokeFmtRewritesFile exercises the fmt round trip: an unformatted schema
// is rewritten in place and then passes --check.
func TestSmokeFmtRewritesFile(t *testing.T) {
	bin := buildGCO(t)
	dir := t.TempDir()

	unformatted := "model User {\n  id String @id\n    email String @unique\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "schema.gcorm"), []byte(unformatted), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runGCO(t, bin, dir, "fmt", "schema.gcorm")
	if res.exitCode != 0 {
		t.Fatalf("fmt exit code = %d\nstderr: %s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "Formatted") {
		t.Errorf("stdout missing %q\ngot: %s", "Formatted", res.stdout)
	}

	data, err := os.ReadFile(filepath.Join(dir, "schema.gcorm"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == unformatted {
		t.Errorf("fmt did not rewrite the file")
	}

	res = runGCO(t, bin, dir, "fmt", "--check", "schema.gcorm")
	if res.exitCode != 0 {
		t.Errorf("fmt --check after fmt exit code = %d\nstderr: %s", res.exitCode, res.stderr)
	}

	// Formatting an already-formatted file must report it as unchanged.
	res = runGCO(t, bin, dir, "fmt", "schema.gcorm")
	if res.exitCode != 0 {
		t.Fatalf("second fmt exit code = %d\nstderr: %s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "Unchanged") {
		t.Errorf("stdout missing %q\ngot: %s", "Unchanged", res.stdout)
	}
}

// TestSmokeInitValidateGenerateWorkflow runs the full getting-started flow in
// one directory: init, then validate and generate using schema discovery.
func TestSmokeInitValidateGenerateWorkflow(t *testing.T) {
	bin := buildGCO(t)
	dir := t.TempDir()

	// The generator requires a Go module to derive import paths.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/smoke\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runGCO(t, bin, dir, "init", "--yes", "--provider", "sqlite")
	if res.exitCode != 0 {
		t.Fatalf("init exit code = %d\nstderr: %s", res.exitCode, res.stderr)
	}

	res = runGCO(t, bin, dir, "validate")
	if res.exitCode != 0 {
		t.Fatalf("validate exit code = %d\nstderr: %s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "Validated 1 schema file(s)") {
		t.Errorf("unexpected validate output: %s", res.stdout)
	}

	res = runGCO(t, bin, dir, "generate")
	if res.exitCode != 0 {
		t.Fatalf("generate exit code = %d\nstderr: %s", res.exitCode, res.stderr)
	}

	for _, f := range []string{
		filepath.Join("gen", "model", "models.go"),
		filepath.Join("gen", "query", "user.go"),
		filepath.Join("gen", "client", "client.go"),
		filepath.Join("gen", "GUIDE.md"),
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected generated file %s: %v", f, err)
		}
	}
}
