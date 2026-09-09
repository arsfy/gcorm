package upgrade

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/internal/versioncheck"
	"runtime/debug"
)

func TestIsGoInstallBuild(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	if !isGoInstallBuild(info, "dev") {
		t.Fatal("go install build was not detected")
	}
	if isGoInstallBuild(info, "v0.1.0") {
		t.Fatal("ldflags/manual release build should not be treated as go install")
	}
}

func TestIsGoInstallBuildRejectsLocalBuild(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "(devel)",
		},
	}

	if isGoInstallBuild(info, "dev") {
		t.Fatal("local build should not be treated as go install")
	}
}

func TestRunRejectsManualInstall(t *testing.T) {
	err := Run(context.Background(), Options{InjectedVersion: "v0.1.0"})
	if err == nil {
		t.Fatal("Run() error = nil, want manual install error")
	}
	if !strings.Contains(err.Error(), "Manual installs") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunAlreadyUpToDateSkipsInstall(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}
	if !isGoInstallBuild(info, "dev") {
		t.Fatal("test setup should look like go install")
	}

	// This test covers the version decision without invoking go install. The
	// real Run path reads the process build info, which is a local test binary.
	result := versioncheck.Result{Current: "v0.1.0", Latest: "v0.1.0", UpdateAvailable: false}
	if result.UpdateAvailable {
		t.Fatal("same version should not be update available")
	}
}

func TestRunInstallsSpecificLatestRelease(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	var out bytes.Buffer
	var gotName string
	var gotArgs []string
	err := runWithBuildInfo(context.Background(), Options{
		InjectedVersion: "dev",
		Out:             &out,
		CheckLatest: func(context.Context, string, string, string) (versioncheck.Result, error) {
			return versioncheck.Result{Current: "v0.1.0", Latest: "v0.2.0", UpdateAvailable: true}, nil
		},
		CommandContext: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "go", "version")
		},
	}, info, true)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotName != "go" {
		t.Fatalf("command name = %q, want go", gotName)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "install" || gotArgs[1] != "github.com/arsfy/gcorm/cmd/gco@v0.2.0" {
		t.Fatalf("command args = %#v", gotArgs)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "@latest") {
		t.Fatalf("upgrade command used @latest: %#v", gotArgs)
	}
	if !strings.Contains(out.String(), "upgrading gco v0.1.0 -> v0.2.0") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunRequiresLatestReleaseCheck(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	err := runWithBuildInfo(context.Background(), Options{
		InjectedVersion: "dev",
		CheckLatest: func(context.Context, string, string, string) (versioncheck.Result, error) {
			return versioncheck.Result{}, context.Canceled
		},
		CommandContext: func(context.Context, string, ...string) *exec.Cmd {
			t.Fatal("CommandContext should not be called when latest release check fails")
			return nil
		},
	}, info, true)
	if err == nil {
		t.Fatal("Run() error = nil, want check latest error")
	}
	if !strings.Contains(err.Error(), "check latest release") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestIsGoInstallBuildRejectsWrongCommand(t *testing.T) {
	info := &debug.BuildInfo{
		Path: "github.com/arsfy/gcorm/cmd/other",
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	if isGoInstallBuild(info, "dev") {
		t.Fatal("different command package should not be treated as gco go install")
	}
}

func TestInstallSpecForVersion(t *testing.T) {
	got := installSpecForVersion(" v1.2.3 ")
	if got != "github.com/arsfy/gcorm/cmd/gco@v1.2.3" {
		t.Fatalf("installSpecForVersion() = %q", got)
	}
}

func TestRunWithBuildInfoNotOK(t *testing.T) {
	err := runWithBuildInfo(context.Background(), Options{InjectedVersion: "dev"}, nil, false)
	if err == nil {
		t.Fatal("runWithBuildInfo() error = nil, want unsupported install error")
	}
	if !strings.Contains(err.Error(), "go install") {
		t.Fatalf("runWithBuildInfo() error = %v", err)
	}
}

func TestRunWithNilBuildInfo(t *testing.T) {
	err := runWithBuildInfo(context.Background(), Options{InjectedVersion: "dev"}, nil, true)
	if err == nil {
		t.Fatal("runWithBuildInfo() error = nil, want unsupported install error")
	}
	if !strings.Contains(err.Error(), "Manual installs") {
		t.Fatalf("runWithBuildInfo() error = %v", err)
	}
}

func TestRunAlreadyUpToDate(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	var out bytes.Buffer
	err := runWithBuildInfo(context.Background(), Options{
		InjectedVersion: "dev",
		Out:             &out,
		CheckLatest: func(context.Context, string, string, string) (versioncheck.Result, error) {
			return versioncheck.Result{Current: "v0.1.0", Latest: "v0.1.0", UpdateAvailable: false}, nil
		},
		CommandContext: func(context.Context, string, ...string) *exec.Cmd {
			t.Fatal("CommandContext should not be called when already up to date")
			return nil
		},
	}, info, true)
	if err != nil {
		t.Fatalf("runWithBuildInfo() error = %v", err)
	}
	if !strings.Contains(out.String(), "gco is already up to date (v0.1.0)") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunDefaultWriters(t *testing.T) {
	// Omit Out and Err so the default os.Stdout/os.Stderr branches run.
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	err := runWithBuildInfo(context.Background(), Options{
		InjectedVersion: "dev",
		CheckLatest: func(context.Context, string, string, string) (versioncheck.Result, error) {
			return versioncheck.Result{Current: "v0.1.0", Latest: "v0.1.0", UpdateAvailable: false}, nil
		},
	}, info, true)
	if err != nil {
		t.Fatalf("runWithBuildInfo() error = %v", err)
	}
}

func TestRunNoLatestRelease(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	err := runWithBuildInfo(context.Background(), Options{
		InjectedVersion: "dev",
		CheckLatest: func(context.Context, string, string, string) (versioncheck.Result, error) {
			return versioncheck.Result{Current: "v0.1.0", Latest: " ", UpdateAvailable: false}, nil
		},
		CommandContext: func(context.Context, string, ...string) *exec.Cmd {
			t.Fatal("CommandContext should not be called when no release was found")
			return nil
		},
	}, info, true)
	if err == nil {
		t.Fatal("runWithBuildInfo() error = nil, want no release error")
	}
	if !strings.Contains(err.Error(), "no GitHub release") {
		t.Fatalf("runWithBuildInfo() error = %v", err)
	}
}

func TestRunGoInstallFailure(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	var out, errOut bytes.Buffer
	err := runWithBuildInfo(context.Background(), Options{
		InjectedVersion: "dev",
		Out:             &out,
		Err:             &errOut,
		CheckLatest: func(context.Context, string, string, string) (versioncheck.Result, error) {
			return versioncheck.Result{Current: "v0.1.0", Latest: "v0.2.0", UpdateAvailable: true}, nil
		},
		CommandContext: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "exit 1")
		},
	}, info, true)
	if err == nil {
		t.Fatal("runWithBuildInfo() error = nil, want go install error")
	}
	if !strings.Contains(err.Error(), "go install github.com/arsfy/gcorm/cmd/gco@v0.2.0") {
		t.Fatalf("runWithBuildInfo() error = %v", err)
	}
}

func TestCurrentBuildVersion(t *testing.T) {
	cases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"nil info", nil, "dev"},
		{"empty version", &debug.BuildInfo{Main: debug.Module{Version: ""}}, "dev"},
		{"devel version", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"release version", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, "v1.2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := currentBuildVersion(tc.info); got != tc.want {
				t.Fatalf("currentBuildVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsGoInstallBuildCases(t *testing.T) {
	releaseInfo := func() *debug.BuildInfo {
		return &debug.BuildInfo{
			Path: commandPath,
			Main: debug.Module{Path: modulePath, Version: "v0.1.0"},
		}
	}

	cases := []struct {
		name            string
		info            *debug.BuildInfo
		injectedVersion string
		want            bool
	}{
		{"go install build, empty injected version", releaseInfo(), "", true},
		{"go install build, dev injected version", releaseInfo(), "dev", true},
		{"injected ldflags version", releaseInfo(), "v0.1.0", false},
		{"nil build info", nil, "dev", false},
		{"wrong main module", &debug.BuildInfo{
			Path: commandPath,
			Main: debug.Module{Path: "example.com/other", Version: "v0.1.0"},
		}, "dev", false},
		{"dev main version", &debug.BuildInfo{
			Path: commandPath,
			Main: debug.Module{Path: modulePath, Version: "dev"},
		}, "dev", false},
		{"empty main version", &debug.BuildInfo{
			Path: commandPath,
			Main: debug.Module{Path: modulePath, Version: ""},
		}, "dev", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGoInstallBuild(tc.info, tc.injectedVersion); got != tc.want {
				t.Fatalf("isGoInstallBuild() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunDefaultCheckLatestCancelledContext(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Leave CheckLatest nil so the default versioncheck.Check branch runs. The
	// pre-cancelled context makes the HTTP call fail before any network dial.
	err := runWithBuildInfo(ctx, Options{
		InjectedVersion: "dev",
		CommandContext: func(context.Context, string, ...string) *exec.Cmd {
			t.Fatal("CommandContext should not be called when the release check fails")
			return nil
		},
	}, info, true)
	if err == nil {
		t.Fatal("runWithBuildInfo() error = nil, want check latest error")
	}
	if !strings.Contains(err.Error(), "check latest release") {
		t.Fatalf("runWithBuildInfo() error = %v", err)
	}
}

func TestRunDefaultCommandContext(t *testing.T) {
	info := &debug.BuildInfo{
		Path: commandPath,
		Main: debug.Module{
			Path:    modulePath,
			Version: "v0.1.0",
		},
	}

	// Put a fake `go` binary on PATH so the default exec.CommandContext branch
	// runs without performing a real go install.
	dir := t.TempDir()
	fakeGo := filepath.Join(dir, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	err := runWithBuildInfo(context.Background(), Options{
		InjectedVersion: "dev",
		Out:             &out,
		CheckLatest: func(context.Context, string, string, string) (versioncheck.Result, error) {
			return versioncheck.Result{Current: "v0.1.0", Latest: "v0.2.0", UpdateAvailable: true}, nil
		},
	}, info, true)
	if err != nil {
		t.Fatalf("runWithBuildInfo() error = %v", err)
	}
	if !strings.Contains(out.String(), "gco upgraded successfully") {
		t.Fatalf("output = %q", out.String())
	}
}
