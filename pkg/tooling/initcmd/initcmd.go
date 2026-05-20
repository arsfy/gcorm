// Package initcmd implements the `gco init` command.
package initcmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Run executes the init command using standard IO.
func Run(args []string) error {
	return runWithIO(args, os.Stdin, os.Stdout)
}

func runWithIO(args []string, in io.Reader, out io.Writer) error {
	opts := initOptions{
		Provider:   "",
		SchemaFile: filepath.Join("schema", "schema.gcorm"),
		EnvVar:     "DATABASE_URL",
		OutputDir:  "./gen",
		Package:    "db",
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--provider":
			if i+1 < len(args) {
				opts.Provider = strings.ToLower(strings.TrimSpace(args[i+1]))
				i++
			}
		case "--schema-file":
			if i+1 < len(args) {
				opts.SchemaFile = strings.TrimSpace(args[i+1])
				i++
			}
		case "--env":
			if i+1 < len(args) {
				opts.EnvVar = strings.TrimSpace(args[i+1])
				i++
			}
		case "--output":
			if i+1 < len(args) {
				opts.OutputDir = strings.TrimSpace(args[i+1])
				i++
			}
		case "--package":
			if i+1 < len(args) {
				opts.Package = strings.TrimSpace(args[i+1])
				i++
			}
		case "--yes":
			opts.NonInteractive = true
		case "--force":
			opts.Force = true
		}
	}

	if opts.NonInteractive {
		if opts.Provider == "" {
			opts.Provider = "postgresql"
		}
	} else {
		reader := bufio.NewReader(in)
		if opts.Provider == "" {
			provider, err := prompt(reader, out, "Choose database provider (postgresql/mysql/sqlite)", "postgresql")
			if err != nil {
				return err
			}
			opts.Provider = strings.ToLower(provider)
		}
		schemaFile, err := prompt(reader, out, "Schema file path", opts.SchemaFile)
		if err != nil {
			return err
		}
		opts.SchemaFile = schemaFile

		envVar, err := prompt(reader, out, "Datasource env variable", opts.EnvVar)
		if err != nil {
			return err
		}
		opts.EnvVar = envVar

		outputDir, err := prompt(reader, out, "Generator output directory", opts.OutputDir)
		if err != nil {
			return err
		}
		opts.OutputDir = outputDir

		pkgName, err := prompt(reader, out, "Generated Go package name", opts.Package)
		if err != nil {
			return err
		}
		opts.Package = pkgName
	}

	if err := validateProvider(opts.Provider); err != nil {
		return err
	}
	if opts.SchemaFile == "" {
		return fmt.Errorf("schema file path cannot be empty")
	}
	if opts.EnvVar == "" {
		return fmt.Errorf("env variable name cannot be empty")
	}
	if opts.OutputDir == "" {
		return fmt.Errorf("output directory cannot be empty")
	}
	if opts.Package == "" {
		return fmt.Errorf("package name cannot be empty")
	}

	if !opts.Force {
		if _, err := os.Stat(opts.SchemaFile); err == nil {
			return fmt.Errorf("schema file already exists: %s (use --force to overwrite)", opts.SchemaFile)
		}
	}

	if err := os.MkdirAll(filepath.Dir(opts.SchemaFile), 0o755); err != nil {
		return fmt.Errorf("create schema directory: %w", err)
	}

	content := renderSchemaTemplate(opts)
	if err := os.WriteFile(opts.SchemaFile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write schema file: %w", err)
	}

	fmt.Fprintf(out, "Initialized schema at %s\n", opts.SchemaFile)
	fmt.Fprintf(out, "Next steps:\n")
	fmt.Fprintf(out, "  1) Set %s in your environment\n", opts.EnvVar)
	fmt.Fprintf(out, "  2) Run: gco validate\n")
	fmt.Fprintf(out, "  3) Run: gco generate\n")
	return nil
}

type initOptions struct {
	Provider       string
	SchemaFile     string
	EnvVar         string
	OutputDir      string
	Package        string
	NonInteractive bool
	Force          bool
}

func prompt(reader *bufio.Reader, out io.Writer, label, def string) (string, error) {
	fmt.Fprintf(out, "%s [%s]: ", label, def)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	v := strings.TrimSpace(line)
	if v == "" {
		return def, nil
	}
	return v, nil
}

func validateProvider(provider string) error {
	switch provider {
	case "postgresql", "mysql", "sqlite":
		return nil
	default:
		return fmt.Errorf("unsupported provider %q (expected postgresql/mysql/sqlite)", provider)
	}
}

func renderSchemaTemplate(opts initOptions) string {
	modelIDType := "String"
	if opts.Provider == "sqlite" {
		modelIDType = "Int"
	}

	idField := "id        " + modelIDType + " @id"
	if modelIDType == "String" {
		idField += " @default(uuid())"
	} else {
		idField += " @default(autoincrement())"
	}

	return fmt.Sprintf(`datasource db {
  provider = %q
  url      = env(%q)
}

generator client {
  provider = "gco-go"
  output   = %q
  package  = %q
}

model User {
  %s
  email     String   @unique
  name      String?
  createdAt DateTime @default(now())
}
`, opts.Provider, opts.EnvVar, opts.OutputDir, opts.Package, idField)
}
