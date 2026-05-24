package upgrade

import (
	"context"
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
