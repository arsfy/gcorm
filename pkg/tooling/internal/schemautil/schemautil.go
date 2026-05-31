package schemautil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arsfy/gcorm/internal/config"
	"github.com/arsfy/gcorm/pkg/schema/compiler"
	"github.com/arsfy/gcorm/pkg/schema/ir"
	"github.com/arsfy/gcorm/pkg/schema/parser"
)

// LoadedSchema is a compiled schema plus a deterministic hash of its source files.
type LoadedSchema struct {
	Schema *ir.Schema
	Hash   string
}

// LoadFromConfig discovers, reads, parses, and compiles schema files from the OS filesystem.
func LoadFromConfig(schemaPath, configPath string) (*LoadedSchema, error) {
	cfg, _, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	var roots []string
	if schemaPath != "" {
		roots = []string{schemaPath}
	} else {
		roots, err = config.DiscoverSchemaRoots(cfg, cwd)
		if err != nil {
			return nil, err
		}
	}

	files, err := config.DiscoverSchemaFiles(roots)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .gcorm schema files found in %v", roots)
	}

	fileContents := make(map[string][]byte, len(files))
	for _, f := range files {
		data, readErr := os.ReadFile(f)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", f, readErr)
		}
		fileContents[f] = data
	}
	return compileFiles(fileContents, "parse error")
}

// LoadFS discovers, reads, parses, and compiles schema files from an fs.FS.
func LoadFS(schemaFS fs.FS, schemaRoot string) (*LoadedSchema, error) {
	files, err := DiscoverFilesFS(schemaFS, schemaRoot)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		root := schemaRoot
		if root == "" {
			root = "."
		}
		return nil, fmt.Errorf("no .gcorm schema files found in %s", root)
	}

	fileContents := make(map[string][]byte, len(files))
	for _, f := range files {
		data, readErr := fs.ReadFile(schemaFS, f)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", f, readErr)
		}
		fileContents[f] = data
	}
	return compileFiles(fileContents, "parse error")
}

// DiscoverFilesFS finds .gcorm files under schemaRoot in an fs.FS.
func DiscoverFilesFS(schemaFS fs.FS, schemaRoot string) ([]string, error) {
	root := strings.TrimSpace(filepath.ToSlash(schemaRoot))
	root = strings.TrimPrefix(root, "./")
	if root == "" {
		root = "."
	}
	root = path.Clean(root)

	info, err := fs.Stat(schemaFS, root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if strings.HasSuffix(root, ".gcorm") {
			return []string{root}, nil
		}
		return nil, fmt.Errorf("schema path is not a .gcorm file: %s", root)
	}

	var files []string
	if err := fs.WalkDir(schemaFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".gcorm") {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func compileFiles(fileContents map[string][]byte, parsePrefix string) (*LoadedSchema, error) {
	ds, parseErr := parser.ParseMulti(fileContents)
	if parseErr != nil {
		return nil, fmt.Errorf("%s: %w", parsePrefix, parseErr)
	}

	result := compiler.Compile(ds)
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("schema compilation failed with %d error(s)", len(result.Errors))
	}
	if result.Schema == nil {
		return nil, fmt.Errorf("no schema produced")
	}
	return &LoadedSchema{Schema: result.Schema, Hash: HashFiles(fileContents)}, nil
}

// HashFiles returns a deterministic SHA-256 hash of schema filenames and contents.
func HashFiles(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(files[name])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
