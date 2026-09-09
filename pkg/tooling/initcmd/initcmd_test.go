package initcmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWithIONonInteractive(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runWithIO([]string{"--yes", "--provider", "postgresql"}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("runWithIO() error: %v", err)
	}

	schemaPath := filepath.Join(dir, "schema", "schema.gcorm")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `provider = "postgresql"`) {
		t.Fatalf("schema content missing provider: %s", content)
	}
	if !strings.Contains(content, `url      = env("DATABASE_URL")`) {
		t.Fatalf("schema content missing env url: %s", content)
	}
	if !strings.Contains(content, `output   = "./gen"`) {
		t.Fatalf("schema content missing output: %s", content)
	}
}

func TestRunWithIOInteractive(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	// provider, schema path, env var, output dir, package
	input := strings.Join([]string{
		"mysql",
		"schema/app.gcorm",
		"APP_DATABASE_URL",
		"./generated",
		"store",
	}, "\n") + "\n"

	var out bytes.Buffer
	err := runWithIO(nil, strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("runWithIO() error: %v", err)
	}

	schemaPath := filepath.Join(dir, "schema", "app.gcorm")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `provider = "mysql"`) {
		t.Fatalf("schema content missing provider: %s", content)
	}
	if !strings.Contains(content, `url      = env("APP_DATABASE_URL")`) {
		t.Fatalf("schema content missing env url: %s", content)
	}
	if !strings.Contains(content, `output   = "./generated"`) {
		t.Fatalf("schema content missing output: %s", content)
	}
	if !strings.Contains(content, `package  = "store"`) {
		t.Fatalf("schema content missing package: %s", content)
	}
}

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunNonInteractive(t *testing.T) {
	dir := chdirTemp(t)

	if err := Run([]string{"--yes"}); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "schema", "schema.gcorm"))
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	if !strings.Contains(string(data), `provider = "postgresql"`) {
		t.Fatalf("expected default postgresql provider, got: %s", data)
	}
}

func TestRunWithIOExistingFileWithoutForce(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll("schema", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("schema", "schema.gcorm"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runWithIO([]string{"--yes", "--provider", "postgresql"}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for existing schema file")
	}
	if !strings.Contains(err.Error(), "use --force") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithIOExistingFileWithForce(t *testing.T) {
	dir := chdirTemp(t)

	schemaPath := filepath.Join(dir, "schema", "schema.gcorm")
	if err := os.MkdirAll(filepath.Dir(schemaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schemaPath, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runWithIO([]string{"--yes", "--force", "--provider", "sqlite"}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("runWithIO() error: %v", err)
	}

	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	if !strings.Contains(string(data), `provider = "sqlite"`) {
		t.Fatalf("schema file was not overwritten: %s", data)
	}
	if !strings.Contains(out.String(), "Initialized schema at") {
		t.Fatalf("expected success output, got: %s", out.String())
	}
}

func TestRunWithIOValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unsupported provider",
			args:    []string{"--yes", "--provider", "oracle"},
			wantErr: `unsupported provider "oracle"`,
		},
		{
			name:    "empty schema file",
			args:    []string{"--yes", "--schema-file", "   "},
			wantErr: "schema file path cannot be empty",
		},
		{
			name:    "empty env var",
			args:    []string{"--yes", "--env", ""},
			wantErr: "env variable name cannot be empty",
		},
		{
			name:    "empty output dir",
			args:    []string{"--yes", "--output", ""},
			wantErr: "output directory cannot be empty",
		},
		{
			name:    "empty package",
			args:    []string{"--yes", "--package", ""},
			wantErr: "package name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chdirTemp(t)
			err := runWithIO(tt.args, strings.NewReader(""), &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestRunWithIOFlagOverrides(t *testing.T) {
	dir := chdirTemp(t)

	var out bytes.Buffer
	err := runWithIO([]string{
		"--yes",
		"--provider", "MySQL",
		"--schema-file", "custom/app.gcorm",
		"--env", "MY_DB_URL",
		"--output", "./out",
		"--package", "data",
		"--force",
	}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("runWithIO() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "custom", "app.gcorm"))
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		`provider = "mysql"`,
		`url      = env("MY_DB_URL")`,
		`output   = "./out"`,
		`package  = "data"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("schema content missing %q: %s", want, content)
		}
	}
}

func TestRunWithIOInteractiveDefaults(t *testing.T) {
	dir := chdirTemp(t)

	// Blank answers for every prompt should fall back to defaults.
	input := "\n\n\n\n\n"
	var out bytes.Buffer
	err := runWithIO(nil, strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("runWithIO() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "schema", "schema.gcorm"))
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `provider = "postgresql"`) {
		t.Fatalf("expected default provider, got: %s", content)
	}
	if !strings.Contains(out.String(), "Choose database provider") {
		t.Fatalf("expected prompts in output, got: %s", out.String())
	}
}

func TestRunWithIOInteractiveEOFWithoutNewline(t *testing.T) {
	dir := chdirTemp(t)

	// Input ends without a trailing newline; prompt must tolerate io.EOF.
	var out bytes.Buffer
	err := runWithIO(nil, strings.NewReader("sqlite"), &out)
	if err != nil {
		t.Fatalf("runWithIO() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "schema", "schema.gcorm"))
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	if !strings.Contains(string(data), `provider = "sqlite"`) {
		t.Fatalf("expected sqlite provider, got: %s", data)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read failure")
}

func TestRunWithIOInteractiveReadError(t *testing.T) {
	chdirTemp(t)

	err := runWithIO(nil, errReader{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "read failure") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// failAfterReader serves data once, then fails on the next read.
type failAfterReader struct {
	data []byte
	done bool
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, fmt.Errorf("read failure")
	}
	r.done = true
	return copy(p, r.data), nil
}

func TestRunWithIOInteractiveReadErrorAtLaterPrompts(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"schema file prompt", "postgresql\n"},
		{"env var prompt", "postgresql\nschema/x.gcorm\n"},
		{"output dir prompt", "postgresql\nschema/x.gcorm\nDB_URL\n"},
		{"package prompt", "postgresql\nschema/x.gcorm\nDB_URL\n./gen\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chdirTemp(t)
			err := runWithIO(nil, &failAfterReader{data: []byte(tt.input)}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "read failure") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRunWithIOSchemaDirCreationFailure(t *testing.T) {
	chdirTemp(t)

	// A regular file blocks creation of the schema directory.
	if err := os.WriteFile("schema", []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runWithIO([]string{"--yes"}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "create schema directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithIOWriteFileFailure(t *testing.T) {
	chdirTemp(t)

	// With --force the stat check is skipped; writing over a directory fails.
	if err := os.MkdirAll(filepath.Join("schema", "schema.gcorm"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := runWithIO([]string{"--yes", "--force"}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "write schema file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithIOFlagWithoutValue(t *testing.T) {
	dir := chdirTemp(t)

	// A trailing flag without a value is ignored; defaults still apply.
	var out bytes.Buffer
	err := runWithIO([]string{"--yes", "--provider"}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("runWithIO() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "schema", "schema.gcorm"))
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	if !strings.Contains(string(data), `provider = "postgresql"`) {
		t.Fatalf("expected default postgresql provider, got: %s", data)
	}
}

func TestValidateProvider(t *testing.T) {
	tests := []struct {
		provider string
		wantErr  bool
	}{
		{"postgresql", false},
		{"mysql", false},
		{"sqlite", false},
		{"", true},
		{"Postgresql", true},
		{"sqlite3", true},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			err := validateProvider(tt.provider)
			if tt.wantErr && err == nil {
				t.Fatalf("validateProvider(%q) expected error, got nil", tt.provider)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateProvider(%q) unexpected error: %v", tt.provider, err)
			}
		})
	}
}

func TestRenderSchemaTemplate(t *testing.T) {
	tests := []struct {
		name     string
		opts     initOptions
		contains []string
	}{
		{
			name: "postgresql uses uuid id",
			opts: initOptions{Provider: "postgresql", EnvVar: "DATABASE_URL", OutputDir: "./gen", Package: "db"},
			contains: []string{
				`provider = "postgresql"`,
				"id        String @id @default(uuid())",
			},
		},
		{
			name: "mysql uses uuid id",
			opts: initOptions{Provider: "mysql", EnvVar: "DATABASE_URL", OutputDir: "./gen", Package: "db"},
			contains: []string{
				`provider = "mysql"`,
				"id        String @id @default(uuid())",
			},
		},
		{
			name: "sqlite uses autoincrement id",
			opts: initOptions{Provider: "sqlite", EnvVar: "DATABASE_URL", OutputDir: "./gen", Package: "db"},
			contains: []string{
				`provider = "sqlite"`,
				"id        Int @id @default(autoincrement())",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := renderSchemaTemplate(tt.opts)
			for _, want := range tt.contains {
				if !strings.Contains(content, want) {
					t.Fatalf("template missing %q:\n%s", want, content)
				}
			}
		})
	}
}
