package upgrade

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"

	"github.com/arsfy/gcorm/internal/versioncheck"
)

const (
	modulePath  = "github.com/arsfy/gcorm"
	commandPath = "github.com/arsfy/gcorm/cmd/gco"
	releasesURL = "https://github.com/arsfy/gcorm/releases"
)

type Options struct {
	InjectedVersion string
	Out             io.Writer
	Err             io.Writer
	CommandContext  func(context.Context, string, ...string) *exec.Cmd
	CheckLatest     func(context.Context, string, string, string) (versioncheck.Result, error)
}

func Run(ctx context.Context, opts Options) error {
	info, ok := debug.ReadBuildInfo()
	return runWithBuildInfo(ctx, opts, info, ok)
}

func runWithBuildInfo(ctx context.Context, opts Options, info *debug.BuildInfo, buildInfoOK bool) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	errOut := opts.Err
	if errOut == nil {
		errOut = os.Stderr
	}

	if !buildInfoOK || !isGoInstallBuild(info, opts.InjectedVersion) {
		return fmt.Errorf("gco upgrade only supports binaries installed with `go install %s@<version>`.\nManual installs must be upgraded manually from %s", commandPath, releasesURL)
	}

	current := currentBuildVersion(info)
	checkLatest := opts.CheckLatest
	if checkLatest == nil {
		checkLatest = versioncheck.Check
	}

	result, err := checkLatest(ctx, current, "arsfy", "gcorm")
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	latest := strings.TrimSpace(result.Latest)
	if latest == "" {
		return fmt.Errorf("no GitHub release was found; upgrade manually from %s", releasesURL)
	}
	if !result.UpdateAvailable {
		fmt.Fprintf(out, "gco is already up to date (%s)\n", current)
		return nil
	}

	installSpec := installSpecForVersion(latest)
	if latest != "" {
		fmt.Fprintf(out, "upgrading gco %s -> %s\n", current, latest)
	}

	commandContext := opts.CommandContext
	if commandContext == nil {
		commandContext = exec.CommandContext
	}
	cmd := commandContext(ctx, "go", "install", installSpec)
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go install %s: %w", installSpec, err)
	}

	fmt.Fprintln(out, "gco upgraded successfully")
	return nil
}

func installSpecForVersion(version string) string {
	return commandPath + "@" + strings.TrimSpace(version)
}

func isGoInstallBuild(info *debug.BuildInfo, injectedVersion string) bool {
	if injectedVersion != "" && injectedVersion != "dev" {
		return false
	}
	if info == nil {
		return false
	}
	return info.Path == commandPath && info.Main.Path == modulePath && isReleaseVersion(info.Main.Version)
}

func currentBuildVersion(info *debug.BuildInfo) string {
	if info == nil || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "dev"
	}
	return info.Main.Version
}

func isReleaseVersion(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "dev" && v != "(devel)"
}
