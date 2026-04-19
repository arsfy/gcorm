// Package generate implements the `gco generate` command.
package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arsfy/gco-orm/internal/config"
	"github.com/arsfy/gco-orm/pkg/codegen/golang"
	"github.com/arsfy/gco-orm/pkg/schema/compiler"
	"github.com/arsfy/gco-orm/pkg/schema/ir"
	"github.com/arsfy/gco-orm/pkg/schema/parser"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Run executes the generate command.
func Run(args []string) error {
	schemaPath := ""
	configPath := ""
	dryRun := false

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
		case "--dry-run":
			dryRun = true
		}
	}

	cfg, cfgPath, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfgPath != "" {
		fmt.Printf("Using config: %s\n", cfgPath)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	var roots []string
	if schemaPath != "" {
		roots = []string{schemaPath}
	} else {
		roots, err = config.DiscoverSchemaRoots(cfg, cwd)
		if err != nil {
			return err
		}
	}

	files, err := config.DiscoverSchemaFiles(roots)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no .gco schema files found in %v", roots)
	}

	fmt.Printf("Found %d schema file(s)\n", len(files))

	// Parse all files.
	fileContents := make(map[string][]byte)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		fileContents[f] = data
	}

	ds, err := parser.ParseMulti(fileContents)
	if err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}

	// Compile.
	result := compiler.Compile(ds)
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "  %v\n", e)
		}
		return fmt.Errorf("schema compilation failed with %d error(s)", len(result.Errors))
	}

	if result.Validation != nil && result.Validation.HasErrors() {
		for _, e := range result.Validation.Errors {
			fmt.Fprintf(os.Stderr, "  %v\n", e)
		}
		return fmt.Errorf("schema validation failed with %d error(s)", len(result.Validation.Errors))
	}

	// Generate code.
	gen := golang.NewGenerator(result.Schema)
	genFiles, err := gen.Generate()
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}

	manifest := gen.Manifest()
	outputDir := manifest.Output
	if outputDir == "" {
		outputDir = "./gen"
	}

	guideDoc := buildGuideDoc(result.Schema)
	genFiles = append(genFiles, &golang.GeneratedFile{
		Path:    "GUIDE.md",
		Content: []byte(guideDoc),
	})

	if dryRun {
		fmt.Printf("Dry run: would generate %d file(s) to %s\n", len(genFiles), outputDir)
		for _, f := range genFiles {
			fmt.Printf("  %s (%d bytes)\n", f.Path, len(f.Content))
		}
		return nil
	}

	// Write files.
	for _, f := range genFiles {
		outPath := filepath.Join(outputDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", outPath, err)
		}
		if err := os.WriteFile(outPath, f.Content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
	}

	fmt.Printf("Generated %d file(s) to %s\n", len(genFiles), outputDir)
	return nil
}

func buildGuideDoc(schema *ir.Schema) string {
	provider := "postgresql"
	if schema != nil && schema.Datasource != nil && schema.Datasource.Provider != "" {
		provider = schema.Datasource.Provider
	}

	models := make([]*ir.Model, 0)
	if schema != nil {
		models = append(models, schema.Models...)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })

	var b strings.Builder
	b.WriteString("# Guide for Generated GCO Client\n\n")
	b.WriteString("This document is generated automatically by `gco generate`.\n")
	b.WriteString("## Imports\n\n")
	b.WriteString("```go\n")
	b.WriteString("import (\n")
	b.WriteString("    \"context\"\n")
	b.WriteString("    \"database/sql\"\n")
	b.WriteString("    \"your-module/gen/client\"\n")
	b.WriteString("    \"your-module/gen/query\"\n")
	b.WriteString("    \"your-module/gen/model\"\n")
	b.WriteString(")\n")
	b.WriteString("```\n\n")
	b.WriteString("## Client Setup\n\n")
	b.WriteString("```go\n")
	b.WriteString("db, err := sql.Open(\"pgx\", os.Getenv(\"DATABASE_URL\"))\n")
	b.WriteString("if err != nil {\n")
	b.WriteString("    panic(err)\n")
	b.WriteString("}\n")
	b.WriteString("defer db.Close()\n\n")
	b.WriteString("c := client.New(db, client.WithDialect(\"")
	b.WriteString(provider)
	b.WriteString("\"))\n")
	b.WriteString("defer c.Close()\n\n")
	b.WriteString("ctx := context.Background()\n")
	b.WriteString("```\n\n")
	b.WriteString("## Recommended Usage Pattern\n\n")
	b.WriteString("Prefer staged builders for readability and composability:\n\n")
	b.WriteString("```go\n")
	b.WriteString("users, err := c.User.Query().\n")
	b.WriteString("    Where(query.User.Email.Contains(\"@example.com\")).\n")
	b.WriteString("    OrderBy(query.User.CreatedAt.Desc()).\n")
	b.WriteString("    Take(20).\n")
	b.WriteString("    Do(ctx)\n")
	b.WriteString("```\n\n")
	b.WriteString("## CRUD Builder Pattern\n\n")
	b.WriteString("- Create one: `c.<Model>.Create().Set(...).Do(ctx)`\n")
	b.WriteString("- Update one: `c.<Model>.Update().Where(...).Set(...).Do(ctx)`\n")
	b.WriteString("- Update many: `c.<Model>.Update().Where(...).Set(...).DoMany(ctx)`\n")
	b.WriteString("- Delete one: `c.<Model>.Delete().Where(...).Do(ctx)`\n")
	b.WriteString("- Delete many: `c.<Model>.Delete().Where(...).DoMany(ctx)`\n\n")

	b.WriteString("## Models and Fields\n\n")
	for _, m := range models {
		b.WriteString("### ")
		b.WriteString(m.Name)
		b.WriteString("\n\n")
		b.WriteString("- Client handle: `c.")
		b.WriteString(m.Name)
		b.WriteString("`\n")
		b.WriteString("- Query namespace: `query.")
		b.WriteString(m.Name)
		b.WriteString("`\n")
		b.WriteString("- Fields:\n")
		for _, f := range m.Fields {
			if f.Type == ir.FieldKindRelation {
				continue
			}
			b.WriteString("  - `")
			b.WriteString(cases.Title(language.Und).String(f.Name))
			b.WriteString("` (")
			b.WriteString(f.ScalarType)
			if f.IsOptional {
				b.WriteString(", optional")
			}
			if f.IsID {
				b.WriteString(", id")
			}
			if f.IsUnique {
				b.WriteString(", unique")
			}
			b.WriteString(")\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("### When generating application code:\n\n")
	b.WriteString("1. Prefer staged builders (`Query/Create/Update/Delete`) over one-shot calls.\n")
	b.WriteString("2. Use `query.<Model>.<Field>` helpers for all conditions and set operations.\n")
	b.WriteString("3. For optional fields, use `Set(value)` for non-null values and `SetNull()` to write NULL.\n")
	b.WriteString("4. Use `DoMany` only when multiple-row side effects are intended.\n")
	b.WriteString("5. Preserve explicit error handling on every DB operation.\n")

	return b.String()
}
