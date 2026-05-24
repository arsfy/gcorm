// Package main implements the gco CLI binary.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/arsfy/gcorm/internal/config"
	"github.com/arsfy/gcorm/internal/upgrade"
	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/parser"
	"github.com/arsfy/gcorm/pkg/tooling/dbpush"
	gcofmt "github.com/arsfy/gcorm/pkg/tooling/fmt"
	"github.com/arsfy/gcorm/pkg/tooling/generate"
	"github.com/arsfy/gcorm/pkg/tooling/initcmd"
	"github.com/arsfy/gcorm/pkg/tooling/introspect"
	"github.com/arsfy/gcorm/pkg/tooling/migrate"
)

var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = initcmd.Run(os.Args[2:])
	case "generate":
		err = generate.Run(os.Args[2:])
	case "fmt":
		err = gcofmt.Run(os.Args[2:])
	case "validate":
		err = runValidate(os.Args[2:])
	case "introspect":
		err = introspect.Run(os.Args[2:])
	case "migrate":
		err = migrate.Run(os.Args[2:])
	case "db":
		if len(os.Args) > 2 && os.Args[2] == "push" {
			err = runDBPush(os.Args[3:])
		} else {
			fmt.Fprintf(os.Stderr, "unknown db subcommand\n")
			os.Exit(1)
		}
	case "version", "--version", "-v":
		printVersion()
	case "upgrade":
		err = upgrade.Run(context.Background(), upgrade.Options{InjectedVersion: Version})
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func currentVersion() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func printVersion() {
	fmt.Printf("gco %s\n", currentVersion())
}

func printUsage() {
	fmt.Println(`gco - GCORM CLI https://github.com/arsfy/gcorm

Usage:
  gco <command> [flags]

Commands:
  init         Initialize a new GCORM schema interactively
  generate     Generate Go client code from schema
  fmt          Format schema files
  validate     Validate schema files
  introspect   Generate schema from existing database
  migrate      Manage database migrations
    diff       Generate migration from schema diff
    dev        Apply migrations in development mode
    deploy     Apply migrations in production mode
    resolve    Resolve migration state
  db push      Push schema changes directly to database
  version      Print version information
  upgrade      Upgrade gco when installed with go install
  help         Show this help message

Flags:
  --schema <path>    Path to schema directory or file
  --config <path>    Path to configuration file`)
}

func runValidate(args []string) error {
	schemaPath := ""
	configPath := ""

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
		}
	}

	cfg, _, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
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
		return fmt.Errorf("no .gcorm schema files found in %v", roots)
	}

	// Parse all schema files.
	fileContents := make(map[string][]byte)
	for _, f := range files {
		data, readErr := os.ReadFile(f)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", f, readErr)
		}
		fileContents[f] = data
	}

	ds, parseErr := parser.ParseMulti(fileContents)
	if parseErr != nil {
		return fmt.Errorf("parse error: %w", parseErr)
	}

	// Compile and validate.
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

	fmt.Printf("Validated %d schema file(s) successfully.\n", len(files))
	return nil
}

func runDBPush(args []string) error {
	return dbpush.Run(args)
}
