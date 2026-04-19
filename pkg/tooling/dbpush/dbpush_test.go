package dbpush

import (
	"net/url"
	"strings"
	"testing"

	"github.com/arsfy/gco-orm/pkg/schema/ir"
	"github.com/arsfy/gco-orm/pkg/tooling/migrate"
)

func TestResolveURLUsesSchemaDatasourceURL(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URL:      "postgresql://postgres:secret@localhost:15432/postgres?schema=public",
		},
		Models: []*ir.Model{{Name: "User"}},
	}

	got, source, err := resolveURL("", schema)
	if err != nil {
		t.Fatalf("resolveURL() error = %v", err)
	}
	if got == "" {
		t.Fatal("resolveURL() returned empty URL")
	}
	parsedURL, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsedURL.Query().Get("schema") != "" {
		t.Fatalf("resolveURL() kept schema query param: %q", got)
	}
	if parsedURL.Query().Get("search_path") != "public" {
		t.Fatalf("resolveURL() search_path = %q, want %q", parsedURL.Query().Get("search_path"), "public")
	}
	if parsedURL.Query().Get("sslmode") != "disable" {
		t.Fatalf("resolveURL() sslmode = %q, want %q", parsedURL.Query().Get("sslmode"), "disable")
	}
	if source != "schema datasource" {
		t.Fatalf("resolveURL() source = %q, want %q", source, "schema datasource")
	}
}

func TestResolveURLUsesDatasourceEnvURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://postgres:secret@localhost:15432/postgres?schema=public")
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URLIsEnv: true,
			EnvVar:   "DATABASE_URL",
		},
		Models: []*ir.Model{{Name: "User"}},
	}

	got, source, err := resolveURL("", schema)
	if err != nil {
		t.Fatalf("resolveURL() error = %v", err)
	}
	if got == "" {
		t.Fatal("resolveURL() returned empty URL")
	}
	parsedURL, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsedURL.Query().Get("search_path") != "public" {
		t.Fatalf("resolveURL() search_path = %q, want %q", parsedURL.Query().Get("search_path"), "public")
	}
	if !strings.Contains(source, `env("DATABASE_URL")`) {
		t.Fatalf("resolveURL() source = %q", source)
	}
}

func TestResolveURLPreservesExistingSearchPathAndSSLMode(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URL:      "postgresql://postgres:secret@db.example.com:5432/postgres?schema=tenant&search_path=custom&sslmode=require",
		},
	}

	got, _, err := resolveURL("", schema)
	if err != nil {
		t.Fatalf("resolveURL() error = %v", err)
	}
	parsedURL, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsedURL.Query().Get("schema") != "" {
		t.Fatalf("resolveURL() kept schema query param: %q", got)
	}
	if parsedURL.Query().Get("search_path") != "custom" {
		t.Fatalf("resolveURL() search_path = %q, want %q", parsedURL.Query().Get("search_path"), "custom")
	}
	if parsedURL.Query().Get("sslmode") != "require" {
		t.Fatalf("resolveURL() sslmode = %q, want %q", parsedURL.Query().Get("sslmode"), "require")
	}
}

func TestResolveSchemaNameUsesDatasourceSchemaFirst(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{Schema: "app"},
	}

	got := resolveSchemaName(schema, "postgresql://postgres:secret@localhost:15432/postgres?search_path=public")
	if got != "app" {
		t.Fatalf("resolveSchemaName() = %q, want %q", got, "app")
	}
}

func TestResolveSchemaNameUsesSearchPathFromURL(t *testing.T) {
	got := resolveSchemaName(nil, "postgresql://postgres:secret@localhost:15432/postgres?search_path=tenant,public")
	if got != "tenant" {
		t.Fatalf("resolveSchemaName() = %q, want %q", got, "tenant")
	}
}

func TestResolveURLAllowsURLFlagOverride(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URLIsEnv: true,
			EnvVar:   "DATABASE_URL",
		},
	}

	got, source, err := resolveURL("postgresql://override", schema)
	if err != nil {
		t.Fatalf("resolveURL() error = %v", err)
	}
	if got != "postgresql://override" {
		t.Fatalf("resolveURL() = %q", got)
	}
	if source != "--url" {
		t.Fatalf("resolveURL() source = %q, want %q", source, "--url")
	}
}

func TestResolveURLReturnsHelpfulErrorWithoutURL(t *testing.T) {
	schema := &ir.Schema{
		Datasource: &ir.Datasource{
			Provider: "postgresql",
			URLIsEnv: true,
			EnvVar:   "DATABASE_URL",
		},
		Models: []*ir.Model{{Name: "User"}},
	}

	errURL, _, err := resolveURL("", schema)
	if err == nil {
		t.Fatal("resolveURL() error = nil, want error")
	}
	if errURL != "" {
		t.Fatalf("resolveURL() URL = %q, want empty", errURL)
	}
	if !strings.Contains(err.Error(), `env("DATABASE_URL")`) {
		t.Fatalf("resolveURL() error = %v", err)
	}
}

func TestSplitStatementsSkipsComments(t *testing.T) {
	stmts, unsupported := splitStatements(`
CREATE TABLE "users" ("id" INTEGER NOT NULL);
-- SQLite: unsupported
ALTER TABLE "users" ADD COLUMN "name" TEXT;
`)
	if len(stmts) != 2 {
		t.Fatalf("len(stmts) = %d, want 2", len(stmts))
	}
	if len(unsupported) != 1 {
		t.Fatalf("len(unsupported) = %d, want 1", len(unsupported))
	}
}

func TestRiskyChanges(t *testing.T) {
	cs := &migrate.Changeset{
		Changes: []migrate.Change{
			{Type: migrate.AddColumn, Model: "users", Field: "name", Rollback: migrate.SafeRollback},
			{Type: migrate.DropColumn, Model: "users", Field: "legacy", Rollback: migrate.DestructiveRollback},
			{Type: migrate.AlterType, Model: "users", Field: "age", Rollback: migrate.ReviewRequired},
		},
	}

	got := riskyChanges(cs)
	if len(got) != 2 {
		t.Fatalf("len(riskyChanges) = %d, want 2", len(got))
	}
}
