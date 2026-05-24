// Package config handles configuration discovery and loading for the GCO CLI.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the GCORM configuration.
type Config struct {
	SchemaRoots       []string          `yaml:"schemaRoots"`
	MigrationDir      string            `yaml:"migrationDir"`
	ShadowDatabaseURL string            `yaml:"shadowDatabaseURL"`
	Generators        map[string]string `yaml:"generators"`
	Format            *FormatConfig     `yaml:"format"`
}

// FormatConfig holds formatting configuration.
type FormatConfig struct {
	IndentWidth int `yaml:"indentWidth"`
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		MigrationDir: "migrations",
		Format: &FormatConfig{
			IndentWidth: 2,
		},
	}
}

// Load discovers and loads configuration following priority order:
// 1. Explicit --config path
// 2. gco.config.yaml or gco.config.yml in current directory
// 3. Recursive parent search (up to 5 levels)
// 4. GCO_CONFIG environment variable
func Load(explicitPath string) (*Config, string, error) {
	if explicitPath != "" {
		cfg, err := loadFile(explicitPath)
		if err != nil {
			return nil, "", fmt.Errorf("load config %s: %w", explicitPath, err)
		}
		return cfg, explicitPath, nil
	}

	// Search current directory and parents.
	dir, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("get working directory: %w", err)
	}

	for i := 0; i < 6; i++ {
		for _, name := range []string{"gco.config.yaml", "gco.config.yml"} {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				cfg, err := loadFile(p)
				if err != nil {
					return nil, "", err
				}
				return cfg, p, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Fallback to environment variable.
	if envPath := os.Getenv("GCO_CONFIG"); envPath != "" {
		cfg, err := loadFile(envPath)
		if err != nil {
			return nil, "", fmt.Errorf("load config from GCO_CONFIG: %w", err)
		}
		return cfg, envPath, nil
	}

	return DefaultConfig(), "", nil
}

func loadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// DiscoverSchemaRoots finds schema root directories.
func DiscoverSchemaRoots(cfg *Config, cwd string) ([]string, error) {
	if len(cfg.SchemaRoots) > 0 {
		var abs []string
		for _, r := range cfg.SchemaRoots {
			if !filepath.IsAbs(r) {
				r = filepath.Join(cwd, r)
			}
			abs = append(abs, filepath.Clean(r))
		}
		return abs, nil
	}

	var found []string
	for _, name := range []string{"schema", "prisma"} {
		p := filepath.Join(cwd, name)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			found = append(found, p)
		}
	}

	if len(found) > 1 {
		return nil, fmt.Errorf("multiple schema directories found (%v); specify --schema or set schemaRoots in config", found)
	}
	if len(found) == 1 {
		return found, nil
	}

	return []string{cwd}, nil
}

// DiscoverSchemaFiles finds all .gcorm files in the given roots with deterministic ordering.
func DiscoverSchemaFiles(roots []string) ([]string, error) {
	excludeDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		".git":         true,
		"migrations":   true,
		"testdata":     true,
	}

	var files []string
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() && excludeDirs[info.Name()] {
				return filepath.SkipDir
			}
			if !info.IsDir() && filepath.Ext(path) == ".gcorm" {
				absPath, err := filepath.Abs(path)
				if err != nil {
					return err
				}
				normalized := filepath.ToSlash(absPath)
				files = append(files, normalized)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", root, err)
		}
	}

	sortStrings(files)
	return files, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
