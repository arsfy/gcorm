package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolveSymlinks returns p with symlinks resolved. On macOS t.TempDir()
// paths contain /var, which os.Getwd reports as /private/var.
func resolveSymlinks(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MigrationDir != "migrations" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "migrations")
	}
	if cfg.Format == nil {
		t.Fatal("Format is nil")
	}
	if cfg.Format.IndentWidth != 2 {
		t.Errorf("IndentWidth = %d, want 2", cfg.Format.IndentWidth)
	}
}

func TestLoadExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.yaml")
	content := `schemaRoots:
  - ./schema
migrationDir: db/migrations
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if path != cfgPath {
		t.Errorf("config path = %q, want %q", path, cfgPath)
	}
	if len(cfg.SchemaRoots) != 1 || cfg.SchemaRoots[0] != "./schema" {
		t.Errorf("SchemaRoots = %v, want [./schema]", cfg.SchemaRoots)
	}
	if cfg.MigrationDir != "db/migrations" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "db/migrations")
	}
}

func TestLoadExplicitErrors(t *testing.T) {
	dir := t.TempDir()

	badYAML := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(badYAML, []byte("migrationDir: [not, a, string]\n  - bad indent"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"missing file", filepath.Join(dir, "does-not-exist.yaml"), "load config"},
		{"invalid yaml", badYAML, "parse config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, path, err := Load(tt.path)
			if err == nil {
				t.Fatalf("Load(%q) expected error, got cfg=%v path=%q", tt.path, cfg, path)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			if cfg != nil || path != "" {
				t.Errorf("expected nil cfg and empty path on error, got cfg=%v path=%q", cfg, path)
			}
		})
	}
}

func TestLoadExplicitKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "partial.yaml")
	if err := os.WriteFile(cfgPath, []byte("schemaRoots:\n  - ./s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// Fields not present in the file keep their defaults.
	if cfg.MigrationDir != "migrations" {
		t.Errorf("MigrationDir = %q, want default %q", cfg.MigrationDir, "migrations")
	}
	if cfg.Format == nil || cfg.Format.IndentWidth != 2 {
		t.Errorf("Format = %+v, want default IndentWidth 2", cfg.Format)
	}
}

func TestLoadAutoDiscover(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gco.config.yaml")
	content := `migrationDir: migs`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	cfg, _, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MigrationDir != "migs" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "migs")
	}
}

func TestLoadDefaultWhenNoConfig(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if path != "" {
		t.Errorf("expected empty path, got %q", path)
	}
	if cfg.MigrationDir != "migrations" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "migrations")
	}
}

func TestLoadYmlExtension(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gco.config.yml")
	if err := os.WriteFile(cfgPath, []byte("migrationDir: yml-migs"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath = resolveSymlinks(t, cfgPath)

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if path != cfgPath {
		t.Errorf("config path = %q, want %q", path, cfgPath)
	}
	if cfg.MigrationDir != "yml-migs" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "yml-migs")
	}
}

func TestLoadParentDirectorySearch(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gco.config.yaml")
	if err := os.WriteFile(cfgPath, []byte("migrationDir: parent-migs"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath = resolveSymlinks(t, cfgPath)

	// Two levels below the config file.
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if path != cfgPath {
		t.Errorf("config path = %q, want %q", path, cfgPath)
	}
	if cfg.MigrationDir != "parent-migs" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "parent-migs")
	}
}

func TestLoadPrefersCurrentDirOverParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gco.config.yaml"), []byte("migrationDir: parent-migs"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	childCfg := filepath.Join(child, "gco.config.yaml")
	if err := os.WriteFile(childCfg, []byte("migrationDir: child-migs"), 0o644); err != nil {
		t.Fatal(err)
	}
	childCfg = resolveSymlinks(t, childCfg)

	oldWd, _ := os.Getwd()
	if err := os.Chdir(child); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	_, path, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if path != childCfg {
		t.Errorf("config path = %q, want %q (current dir takes priority)", path, childCfg)
	}
}

func TestLoadEnvVarFallback(t *testing.T) {
	dir := t.TempDir()
	envCfg := filepath.Join(dir, "env.yaml")
	if err := os.WriteFile(envCfg, []byte("migrationDir: env-migs"), 0o644); err != nil {
		t.Fatal(err)
	}

	// cwd has no config file, so GCO_CONFIG is consulted.
	empty := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	t.Setenv("GCO_CONFIG", envCfg)

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if path != envCfg {
		t.Errorf("config path = %q, want %q", path, envCfg)
	}
	if cfg.MigrationDir != "env-migs" {
		t.Errorf("MigrationDir = %q, want %q", cfg.MigrationDir, "env-migs")
	}
}

func TestLoadEnvVarMissingFile(t *testing.T) {
	empty := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	t.Setenv("GCO_CONFIG", filepath.Join(empty, "missing.yaml"))

	_, _, err := Load("")
	if err == nil {
		t.Fatal("Load() expected error for missing GCO_CONFIG file")
	}
	if !strings.Contains(err.Error(), "GCO_CONFIG") {
		t.Errorf("error = %q, want it to mention GCO_CONFIG", err)
	}
}

func TestLoadAutoDiscoverParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gco.config.yaml"), []byte("format:\n  indentWidth: \"oops\""), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	_, _, err := Load("")
	if err == nil {
		t.Fatal("Load() expected error for invalid discovered config")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Errorf("error = %q, want it to contain %q", err, "parse config")
	}
}

func TestDiscoverSchemaRootsFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{SchemaRoots: []string{"schema", "prisma"}}
	roots, err := DiscoverSchemaRoots(cfg, dir, "")
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots count = %d, want 2", len(roots))
	}
}

func TestDiscoverSchemaRootsAbsolute(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "schemas")
	cfg := &Config{SchemaRoots: []string{abs}}

	roots, err := DiscoverSchemaRoots(cfg, "/some/unrelated/cwd", "")
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	if len(roots) != 1 || roots[0] != filepath.Clean(abs) {
		t.Errorf("roots = %v, want [%s]", roots, abs)
	}
}

func TestDiscoverSchemaRootsRelativeJoinedWithCwd(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{SchemaRoots: []string{"a/../schema"}}

	roots, err := DiscoverSchemaRoots(cfg, dir, "")
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	if len(roots) != 1 || roots[0] != filepath.Join(dir, "schema") {
		t.Errorf("roots = %v, want [%s]", roots, filepath.Join(dir, "schema"))
	}
}

func TestDiscoverSchemaRootsAutoDiscover(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "schema"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	roots, err := DiscoverSchemaRoots(cfg, dir, "")
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("roots count = %d, want 1", len(roots))
	}
	if filepath.Base(roots[0]) != "schema" {
		t.Errorf("root = %q, want schema dir", roots[0])
	}
}

func TestDiscoverSchemaRootsConflict(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "schema"), 0o755)
	os.Mkdir(filepath.Join(dir, "prisma"), 0o755)

	cfg := &Config{}
	_, err := DiscoverSchemaRoots(cfg, dir, "")
	if err == nil {
		t.Fatal("expected error for conflicting schema dirs")
	}
}

func TestDiscoverSchemaRootsFallbackToCwd(t *testing.T) {
	dir := t.TempDir()
	// No schema/ or prisma/ directory, no schemaRoots configured.
	cfg := &Config{}

	roots, err := DiscoverSchemaRoots(cfg, dir, "")
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	if len(roots) != 1 || roots[0] != dir {
		t.Errorf("roots = %v, want [%s]", roots, dir)
	}
}

func TestDiscoverSchemaRootsIgnoresFiles(t *testing.T) {
	dir := t.TempDir()
	// A regular file named "schema" must not be treated as a schema directory.
	if err := os.WriteFile(filepath.Join(dir, "schema"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	roots, err := DiscoverSchemaRoots(cfg, dir, "")
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	if len(roots) != 1 || roots[0] != dir {
		t.Errorf("roots = %v, want fallback [%s]", roots, dir)
	}
}

func TestDiscoverSchemaFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "models.gcorm"), []byte("model X {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "enums.gcorm"), []byte("enum Y { A }"), 0o644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hi"), 0o644)

	files, err := DiscoverSchemaFiles([]string{dir})
	if err != nil {
		t.Fatalf("DiscoverSchemaFiles() error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files count = %d, want 2", len(files))
	}
}

func TestDiscoverSchemaFilesWalkError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-dir")

	_, err := DiscoverSchemaFiles([]string{missing})
	if err == nil {
		t.Fatal("DiscoverSchemaFiles() expected error for missing root")
	}
	if !strings.Contains(err.Error(), "walk") {
		t.Errorf("error = %q, want it to contain %q", err, "walk")
	}
}

func TestDiscoverSchemaFilesSortedAndNested(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "zeta.gcorm"), []byte("model Z {}"), 0o644)
	os.WriteFile(filepath.Join(sub, "alpha.gcorm"), []byte("model A {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "beta.gcorm"), []byte("model B {}"), 0o644)

	files, err := DiscoverSchemaFiles([]string{dir})
	if err != nil {
		t.Fatalf("DiscoverSchemaFiles() error: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("files count = %d, want 3", len(files))
	}
	for i := 1; i < len(files); i++ {
		if files[i] < files[i-1] {
			t.Errorf("files not sorted: %v", files)
			break
		}
	}
	for _, f := range files {
		if !filepath.IsAbs(f) {
			t.Errorf("file %q is not absolute", f)
		}
	}
}

func TestDiscoverSchemaFilesMultipleRoots(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	os.WriteFile(filepath.Join(root1, "one.gcorm"), []byte("model A {}"), 0o644)
	os.WriteFile(filepath.Join(root2, "two.gcorm"), []byte("model B {}"), 0o644)

	files, err := DiscoverSchemaFiles([]string{root1, root2})
	if err != nil {
		t.Fatalf("DiscoverSchemaFiles() error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files count = %d, want 2", len(files))
	}
}

func TestDiscoverSchemaFilesExcludesVendor(t *testing.T) {
	dir := t.TempDir()
	vendorDir := filepath.Join(dir, "vendor")
	os.Mkdir(vendorDir, 0o755)
	os.WriteFile(filepath.Join(vendorDir, "skip.gcorm"), []byte("model X {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "keep.gcorm"), []byte("model Y {}"), 0o644)

	files, err := DiscoverSchemaFiles([]string{dir})
	if err != nil {
		t.Fatalf("DiscoverSchemaFiles() error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files count = %d, want 1 (vendor should be excluded)", len(files))
	}
}

func TestConfigDir(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name       string
		configPath string
		want       string
	}{
		{"empty path means no config", "", ""},
		{"absolute path", filepath.Join(dir, "gco.config.yaml"), dir},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConfigDir(tt.configPath)
			if err != nil {
				t.Fatalf("ConfigDir() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ConfigDir(%q) = %q, want %q", tt.configPath, got, tt.want)
			}
		})
	}
}

func TestConfigDirRelativePath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gco.config.yaml")
	if err := os.WriteFile(cfgPath, []byte("migrationDir: migs"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	got, err := ConfigDir("gco.config.yaml")
	if err != nil {
		t.Fatalf("ConfigDir() error: %v", err)
	}
	if want := resolveSymlinks(t, dir); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

// Relative schemaRoots must be resolved against the directory containing the
// configuration file, not the process working directory: config.Load searches
// parent directories, so the two frequently differ.
func TestDiscoverSchemaRootsRelativeToConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gco.config.yaml")
	cfg := &Config{SchemaRoots: []string{"schema"}}

	roots, err := DiscoverSchemaRoots(cfg, "/some/unrelated/cwd", cfgPath)
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	want := filepath.Join(dir, "schema")
	if len(roots) != 1 || roots[0] != want {
		t.Errorf("roots = %v, want [%s]", roots, want)
	}
}

// Without a loaded configuration file, relative schemaRoots keep resolving
// against the working directory.
func TestDiscoverSchemaRootsRelativeWithoutConfigUsesCwd(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{SchemaRoots: []string{"schema"}}

	roots, err := DiscoverSchemaRoots(cfg, dir, "")
	if err != nil {
		t.Fatalf("DiscoverSchemaRoots() error: %v", err)
	}
	want := filepath.Join(dir, "schema")
	if len(roots) != 1 || roots[0] != want {
		t.Errorf("roots = %v, want [%s]", roots, want)
	}
}

// A symlinked root directory must be followed; filepath.Walk uses Lstat and
// would otherwise visit only the symlink itself without descending.
func TestDiscoverSchemaFilesSymlinkRoot(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "models.gcorm"), []byte("model X {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	files, err := DiscoverSchemaFiles([]string{link})
	if err != nil {
		t.Fatalf("DiscoverSchemaFiles() error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files count = %d, want 1 (symlink root should be followed)", len(files))
	}
	if filepath.Base(files[0]) != "models.gcorm" {
		t.Errorf("file = %q, want models.gcorm", files[0])
	}
}
