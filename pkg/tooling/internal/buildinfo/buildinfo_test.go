package buildinfo

import "testing"

func TestVersion(t *testing.T) {
	got := Version()
	if got == "" {
		t.Fatal("Version() returned empty string")
	}
	// In a `go test` binary the main module is built in development mode, so
	// no release version is available from build metadata.
	if got != "dev" {
		t.Errorf("Version() in test binary: got %q, want %q", got, "dev")
	}
}

func TestIsReleaseVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"dev", "dev", false},
		{"devel marker", "(devel)", false},
		{"release tag", "v1.2.3", true},
		{"release tag with surrounding spaces", "  v1.2.3  ", true},
		{"pseudo-version", "v0.0.0-20240101000000-abcdef123456", true},
		{"pre-release", "v2.0.0-rc.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReleaseVersion(tt.version); got != tt.want {
				t.Errorf("isReleaseVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}
