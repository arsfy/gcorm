// Package generate implements the `gco generate` command.
package generate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arsfy/gco-orm/internal/config"
	"github.com/arsfy/gco-orm/pkg/codegen/golang"
	"github.com/arsfy/gco-orm/pkg/schema/compiler"
	"github.com/arsfy/gco-orm/pkg/schema/parser"
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
