package buildinfo

import (
	"runtime/debug"
	"strings"
)

const modulePath = "github.com/arsfy/gcorm"

// Version returns the GCORM module version from Go build metadata.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return "dev"
	}
	if info.Main.Path == modulePath && isReleaseVersion(info.Main.Version) {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath && isReleaseVersion(dep.Version) {
			return dep.Version
		}
	}
	return "dev"
}

func isReleaseVersion(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "dev" && v != "(devel)"
}
