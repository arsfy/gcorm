package initcmd

import (
	"bytes"
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

	schemaPath := filepath.Join(dir, "schema", "schema.gco")
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
		"schema/app.gco",
		"APP_DATABASE_URL",
		"./generated",
		"store",
	}, "\n") + "\n"

	var out bytes.Buffer
	err := runWithIO(nil, strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("runWithIO() error: %v", err)
	}

	schemaPath := filepath.Join(dir, "schema", "app.gco")
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
	if err := os.WriteFile(filepath.Join("schema", "schema.gco"), []byte("existing"), 0o644); err != nil {
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
