// Package migrate implements the `gco migrate` commands for database migration management.
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arsfy/gcorm/internal/config"
	"github.com/arsfy/gcorm/pkg/schema/ast"
	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/tooling/internal/schemautil"
)

// MigrationManifest describes a single migration.
type MigrationManifest struct {
	ID             string         `json:"id"`
	Description    string         `json:"description"`
	Checksum       string         `json:"checksum"`
	CreatedAt      time.Time      `json:"createdAt"`
	ToolVersion    string         `json:"toolVersion"`
	DestructiveOps []string       `json:"destructiveOps,omitempty"`
	ReviewRequired bool           `json:"reviewRequired"`
	Changes        []ChangeRecord `json:"changes,omitempty"`
	Models         []string       `json:"models,omitempty"`
	SchemaSnapshot *ir.Schema     `json:"schemaSnapshot,omitempty"`
}

// ChangeRecord stores a single schema change in the migration manifest.
type ChangeRecord struct {
	Type     string `json:"type"`
	Model    string `json:"model"`
	Schema   string `json:"schema,omitempty"`
	Field    string `json:"field,omitempty"`
	Rollback string `json:"rollback"`
}

// MigrationRecord tracks an applied migration in the database.
type MigrationRecord struct {
	ID              string `json:"id"`
	Checksum        string `json:"checksum"`
	AppliedAt       string `json:"appliedAt"`
	ExecutionTimeMs int64  `json:"executionTimeMs"`
	Status          string `json:"status"` // "applied", "failed", "rolled_back"
	ToolVersion     string `json:"toolVersion"`
}

// Run executes the migrate subcommand.
func Run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing migrate subcommand (diff, dev, deploy, resolve, init-sql)")
	}

	switch args[0] {
	case "diff":
		return runDiff(args[1:])
	case "init-sql":
		return runInitSQL(args[1:])
	case "dev":
		return runDev(args[1:])
	case "deploy":
		return runDeploy(args[1:])
	case "resolve":
		return runResolve(args[1:])
	default:
		return fmt.Errorf("unknown migrate subcommand: %s", args[0])
	}
}

func runDiff(args []string) error {
	name := "migration"
	migrationDir := "migrations"
	migrationDirSet := false
	schemaPath := ""
	configPath := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case "--dir":
			if i+1 < len(args) {
				migrationDir = args[i+1]
				migrationDirSet = true
				i++
			}
		case "--schema":
			if i+1 < len(args) {
				schemaPath = args[i+1]
				i++
			}
		case "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		}
	}
	if err := validateMigrationName(name); err != nil {
		return err
	}

	// Load config.
	cfg, cfgPath, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.MigrationDir != "" && !migrationDirSet {
		migrationDir = cfg.MigrationDir
		if !filepath.IsAbs(migrationDir) {
			// Relative migrationDir is resolved against the directory
			// containing the configuration file.
			dir, err := config.ConfigDir(cfgPath)
			if err != nil {
				return err
			}
			if dir != "" {
				migrationDir = filepath.Join(dir, migrationDir)
			}
		}
	}

	loaded, err := schemautil.LoadFromConfig(schemaPath, configPath)
	if err != nil {
		return err
	}
	newSchema := loaded.Schema

	// Load previous schema (nil when no prior migrations exist).
	prevSchema, err := loadPreviousSchema(migrationDir)
	if err != nil {
		return err
	}

	// Diff.
	cs := Diff(prevSchema, newSchema)
	if len(cs.Changes) == 0 {
		fmt.Println("No schema changes detected.")
		return nil
	}

	// Generate DDL.
	dialect := detectDialectFromSchema(newSchema)
	gen := DDLGenerator{Dialect: dialect, Schema: newSchema}
	upSQL := gen.GenerateUp(cs)
	downSQL := gen.GenerateDown(cs)
	orderedChanges := gen.orderedChanges(cs)

	// Write migration files.
	timestamp := time.Now().UTC().Format("20060102_150405")
	dirName, migPath, err := createMigrationDirectory(migrationDir, timestamp, name)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(migPath, "up.sql"), []byte(upSQL), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(migPath, "down.sql"), []byte(downSQL), 0o644); err != nil {
		return err
	}

	// Build manifest with change records.
	var changeRecords []ChangeRecord
	var destructiveOps []string
	reviewRequired := false

	for _, c := range orderedChanges {
		changeRecords = append(changeRecords, ChangeRecord{
			Type:     string(c.Type),
			Model:    c.Model,
			Schema:   c.Schema,
			Field:    c.Field,
			Rollback: string(c.Rollback),
		})
		if c.Rollback == DestructiveRollback {
			desc := string(c.Type) + " " + c.Model
			if c.Field != "" {
				desc += "." + c.Field
			}
			destructiveOps = append(destructiveOps, desc)
		}
		if c.Rollback == ReviewRequired {
			reviewRequired = true
		}
	}

	snapshot, err := credentialFreeSchemaSnapshot(newSchema)
	if err != nil {
		return fmt.Errorf("build schema snapshot: %w", err)
	}

	manifest := MigrationManifest{
		ID:             dirName,
		Description:    name,
		Checksum:       checksumString(upSQL),
		CreatedAt:      time.Now().UTC(),
		ToolVersion:    "0.1.0",
		DestructiveOps: destructiveOps,
		ReviewRequired: reviewRequired,
		Changes:        changeRecords,
		Models:         collectModelNames(newSchema),
		SchemaSnapshot: snapshot,
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(migPath, "manifest.json"), manifestJSON, 0o644); err != nil {
		return err
	}

	// Print summary.
	fmt.Printf("Created migration %s with %d change(s):\n", dirName, len(cs.Changes))
	for _, c := range orderedChanges {
		if c.Schema != "" {
			fmt.Printf("  %s %s.%s", c.Type, c.Schema, c.Model)
		} else {
			fmt.Printf("  %s %s", c.Type, c.Model)
		}
		if c.Field != "" {
			fmt.Printf(".%s", c.Field)
		}
		if c.Rollback == DestructiveRollback {
			fmt.Printf(" [DESTRUCTIVE]")
		}
		fmt.Println()
	}
	return nil
}

func runInitSQL(args []string) error {
	schemaPath := ""
	configPath := ""
	outputPath := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--schema":
			if i+1 < len(args) {
				schemaPath = args[i+1]
				i++
			}
		case "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		case "--output", "-o":
			if i+1 < len(args) {
				outputPath = args[i+1]
				i++
			}
		}
	}

	loaded, err := schemautil.LoadFromConfig(schemaPath, configPath)
	if err != nil {
		return err
	}
	newSchema := loaded.Schema

	cs := Diff(nil, newSchema)
	dialect := detectDialectFromSchema(newSchema)
	gen := DDLGenerator{Dialect: dialect, Schema: newSchema}
	sqlText := strings.TrimRight(gen.GenerateUp(cs), "\n")
	if sqlText != "" {
		sqlText += "\n"
	}

	if outputPath == "" {
		fmt.Print(sqlText)
		return nil
	}

	if dir := filepath.Dir(outputPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	if err := os.WriteFile(outputPath, []byte(sqlText), 0o644); err != nil {
		return fmt.Errorf("write init SQL: %w", err)
	}
	fmt.Printf("Wrote init SQL for %d model(s) to %s\n", len(newSchema.Models), outputPath)
	return nil
}

func runDev(args []string) error {
	name := "migration"
	migrationDir := "migrations"
	schemaPath := ""
	configPath := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case "--dir":
			if i+1 < len(args) {
				migrationDir = args[i+1]
				i++
			}
		case "--schema":
			if i+1 < len(args) {
				schemaPath = args[i+1]
				i++
			}
		case "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		}
	}

	// Reuse diff logic to generate migration.
	diffArgs := []string{"--name", name, "--dir", migrationDir}
	if schemaPath != "" {
		diffArgs = append(diffArgs, "--schema", schemaPath)
	}
	if configPath != "" {
		diffArgs = append(diffArgs, "--config", configPath)
	}

	if err := runDiff(diffArgs); err != nil {
		return err
	}

	fmt.Println("Would apply migration to dev database.")
	fmt.Println("Note: actual database execution requires a driver connection.")
	return nil
}

func runDeploy(args []string) error {
	migrationDir := "migrations"
	for i := 0; i < len(args); i++ {
		if args[i] == "--dir" && i+1 < len(args) {
			migrationDir = args[i+1]
			i++
		}
	}

	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No migrations directory found.")
			return nil
		}
		return err
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(migrationDir, e.Name(), "manifest.json")
		if _, err := os.Stat(manifestPath); err != nil {
			continue
		}
		count++
		fmt.Printf("  %s\n", e.Name())
	}

	if count == 0 {
		fmt.Println("No pending migrations.")
	} else {
		fmt.Printf("Found %d migration(s). Apply requires database connection.\n", count)
	}
	return nil
}

func runResolve(args []string) error {
	migrationDir := "migrations"
	action := ""
	migrationId := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--applied":
			action = "applied"
			if i+1 < len(args) {
				migrationId = args[i+1]
				i++
			}
		case "--rolled-back":
			action = "rolled_back"
			if i+1 < len(args) {
				migrationId = args[i+1]
				i++
			}
		case "--dir":
			if i+1 < len(args) {
				migrationDir = args[i+1]
				i++
			}
		}
	}

	if action == "" || migrationId == "" {
		return fmt.Errorf("usage: gco migrate resolve --applied|--rolled-back <migration_id> [--dir <path>]")
	}

	migPath := filepath.Join(migrationDir, migrationId)
	if _, err := os.Stat(migPath); err != nil {
		return fmt.Errorf("migration %q not found in %s", migrationId, migrationDir)
	}

	fmt.Printf("Resolved migration %s as %s\n", migrationId, action)
	fmt.Println("Note: metadata update requires database connection.")
	return nil
}

// loadPreviousSchema loads the schema state from the latest migration
// manifest. It returns nil when no prior migrations exist. Manifests created by
// older versions without a snapshot cannot safely serve as a diff baseline.
func loadPreviousSchema(migrationDir string) (*ir.Schema, error) {
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	type manifestCandidate struct {
		dir      string
		manifest MigrationManifest
	}
	var latest *manifestCandidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(migrationDir, e.Name(), "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read migration manifest %s: %w", manifestPath, err)
		}
		var manifest MigrationManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("parse migration manifest %s: %w", manifestPath, err)
		}
		candidate := &manifestCandidate{dir: e.Name(), manifest: manifest}
		if latest == nil || candidate.manifest.CreatedAt.After(latest.manifest.CreatedAt) ||
			candidate.manifest.CreatedAt.Equal(latest.manifest.CreatedAt) && candidate.dir > latest.dir {
			latest = candidate
		}
	}
	if latest == nil {
		return nil, nil
	}
	if latest.manifest.SchemaSnapshot == nil {
		return nil, fmt.Errorf("latest migration %q has no schema snapshot; create a new baseline migration before generating an incremental diff", latest.dir)
	}
	return latest.manifest.SchemaSnapshot, nil
}

func validateMigrationName(name string) error {
	if name == "" {
		return fmt.Errorf("migration name cannot be empty")
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("invalid migration name %q: use only letters, numbers, '-' and '_'", name)
	}
	return nil
}

func createMigrationDirectory(migrationDir, timestamp, name string) (string, string, error) {
	if err := os.MkdirAll(migrationDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create migration directory: %w", err)
	}

	base := fmt.Sprintf("%s_%s", timestamp, name)
	for suffix := 0; ; suffix++ {
		dirName := base
		if suffix > 0 {
			dirName = fmt.Sprintf("%s_%03d", base, suffix)
		}
		migPath := filepath.Join(migrationDir, dirName)
		if err := os.Mkdir(migPath, 0o755); err == nil {
			return dirName, migPath, nil
		} else if !os.IsExist(err) {
			return "", "", fmt.Errorf("create migration directory: %w", err)
		}
	}
}

func credentialFreeSchemaSnapshot(schema *ir.Schema) (*ir.Schema, error) {
	if schema == nil {
		return nil, nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var snapshot ir.Schema
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Datasource != nil {
		snapshot.Datasource.URL = ""
		snapshot.Datasource.URLIsEnv = false
		snapshot.Datasource.EnvVar = ""
	}
	snapshot.Generators = nil
	clearSchemaSourceLocations(&snapshot)
	return &snapshot, nil
}

func clearSchemaSourceLocations(schema *ir.Schema) {
	if schema == nil {
		return
	}
	if schema.Datasource != nil {
		schema.Datasource.Span = ast.Span{}
	}
	for _, model := range schema.Models {
		if model == nil {
			continue
		}
		model.Span = ast.Span{}
		for _, field := range model.Fields {
			if field != nil {
				field.Span = ast.Span{}
			}
		}
		for _, relation := range model.Relations {
			if relation != nil && relation.Field != nil {
				relation.Field.Span = ast.Span{}
			}
		}
		for _, index := range model.Indexes {
			if index != nil {
				index.Span = ast.Span{}
			}
		}
	}
	for _, enum := range schema.Enums {
		if enum != nil {
			enum.Span = ast.Span{}
		}
	}
}

// detectDialectFromSchema returns the SQL dialect from the datasource provider,
// defaulting to "postgresql" when the schema has no datasource.
func detectDialectFromSchema(schema *ir.Schema) string {
	if schema != nil && schema.Datasource != nil {
		return schema.Datasource.Provider
	}
	return "postgresql"
}

// collectModelNames returns a sorted list of model names from a schema.
func collectModelNames(schema *ir.Schema) []string {
	if schema == nil {
		return nil
	}
	names := make([]string, 0, len(schema.Models))
	for _, m := range schema.Models {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	return names
}

func checksumString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// CreateMigrationsTable returns the SQL to create the migrations metadata table.
func CreateMigrationsTable(dialect string) string {
	switch dialect {
	case "postgresql":
		return `CREATE TABLE IF NOT EXISTS __gco_migrations (
	id TEXT PRIMARY KEY,
	checksum TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	execution_time_ms BIGINT NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'applied',
	tool_version TEXT NOT NULL
);`
	case "mysql":
		return `CREATE TABLE IF NOT EXISTS __gco_migrations (
	id VARCHAR(255) PRIMARY KEY,
	checksum VARCHAR(255) NOT NULL,
	applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
	execution_time_ms BIGINT NOT NULL DEFAULT 0,
	status VARCHAR(50) NOT NULL DEFAULT 'applied',
	tool_version VARCHAR(50) NOT NULL
);`
	default: // sqlite
		return `CREATE TABLE IF NOT EXISTS __gco_migrations (
	id TEXT PRIMARY KEY,
	checksum TEXT NOT NULL,
	applied_at TEXT NOT NULL DEFAULT (datetime('now')),
	execution_time_ms INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'applied',
	tool_version TEXT NOT NULL
);`
	}
}
