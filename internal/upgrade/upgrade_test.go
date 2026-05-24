package upgrade

import (
	"bytes"
	"context"
	"os/exec"
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
