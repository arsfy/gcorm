package versioncheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckReportsUpdateAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/arsfy/gcorm/releases/latest" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
	}))
	defer server.Close()

	result, err := Client{BaseURL: server.URL}.Check(context.Background(), "v0.1.0", "arsfy", "gcorm")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable {
		t.Fatalf("UpdateAvailable = false, want true: %#v", result)
	}
	if result.Latest != "v0.2.0" {
		t.Fatalf("Latest = %q, want v0.2.0", result.Latest)
	}
}

func TestCheckDoesNotReportUpdateForDevVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
	}))
	defer server.Close()

	result, err := Client{BaseURL: server.URL}.Check(context.Background(), "dev", "arsfy", "gcorm")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("UpdateAvailable = true for dev version")
	}
}

func TestLatestReleaseNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	latest, err := Client{BaseURL: server.URL}.LatestRelease(context.Background(), "arsfy", "gcorm")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "" {
		t.Fatalf("latest = %q, want empty", latest)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a    string
		b    string
		want int
	}{
		{"v1.2.0", "v1.1.9", 1},
		{"1.2.0", "v1.2.0", 0},
		{"v1.2.0", "v1.2.1", -1},
		{"v1.2", "v1.2.0", 0},
		{"dev", "v1.0.0", 0},
	}
	for _, tc := range cases {
		got := compareVersions(tc.a, tc.b)
		if got != tc.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCheckPropagatesLatestReleaseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	result, err := Client{BaseURL: server.URL}.Check(context.Background(), "v0.1.0", "arsfy", "gcorm")
	if err == nil {
		t.Fatal("Check() error = nil, want latest release error")
	}
	if result.Current != "v0.1.0" {
		t.Fatalf("Result.Current = %q, want v0.1.0", result.Current)
	}
	if result.Latest != "" || result.UpdateAvailable {
		t.Fatalf("Result = %#v, want zero Latest and no update", result)
	}
}

func TestCheckPackageLevelCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// A pre-cancelled context fails before any network dial happens, so this
	// exercises the package-level Check without external network access.
	_, err := Check(ctx, "v0.1.0", "arsfy", "gcorm")
	if err == nil {
		t.Fatal("Check() error = nil, want context canceled error")
	}
}

func TestLatestReleaseServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := Client{BaseURL: server.URL}.LatestRelease(context.Background(), "arsfy", "gcorm")
	if err == nil {
		t.Fatal("LatestRelease() error = nil, want status error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("LatestRelease() error = %v", err)
	}
}

func TestLatestReleaseInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer server.Close()

	_, err := Client{BaseURL: server.URL}.LatestRelease(context.Background(), "arsfy", "gcorm")
	if err == nil {
		t.Fatal("LatestRelease() error = nil, want JSON decode error")
	}
}

func TestLatestReleaseTransportError(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}

	_, err := Client{BaseURL: "http://127.0.0.1:1", HTTPClient: client}.LatestRelease(context.Background(), "arsfy", "gcorm")
	if err == nil {
		t.Fatal("LatestRelease() error = nil, want transport error")
	}
}

func TestLatestReleaseInvalidBaseURL(t *testing.T) {
	// A control character makes http.NewRequestWithContext fail before any I/O.
	_, err := Client{BaseURL: "http://example.com/\x7f"}.LatestRelease(context.Background(), "arsfy", "gcorm")
	if err == nil {
		t.Fatal("LatestRelease() error = nil, want request build error")
	}
}

func TestLatestReleaseHeadersAndTrailingSlash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "gcorm" {
			t.Errorf("User-Agent header = %q", got)
		}
		_, _ = w.Write([]byte(`{"tag_name":"  v0.3.0  "}`))
	}))
	defer server.Close()

	latest, err := Client{BaseURL: server.URL + "/"}.LatestRelease(context.Background(), "arsfy", "gcorm")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "v0.3.0" {
		t.Fatalf("latest = %q, want v0.3.0", latest)
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"v1.2.3", []int{1, 2, 3}},
		{"1.2.3", []int{1, 2, 3}},
		{"refs/tags/v1.2.3", []int{1, 2, 3}},
		{" v1.2.3 ", []int{1, 2, 3}},
		{"v1.2.3-rc.1", []int{1, 2, 3}},
		{"v1..2", []int{1, 0, 2}},
		{"", nil},
		{"dev", nil},
		{"(devel)", nil},
		{"v1.x.3", nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := parseVersion(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseVersion(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parseVersion(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestCompareVersionsUnequalLengths(t *testing.T) {
	cases := []struct {
		a    string
		b    string
		want int
	}{
		{"v1.2.1", "v1.2", 1},
		{"v1.2", "v1.2.1", -1},
		{"v1.2.3-rc.1", "v1.2.3", 0},
		{"garbage", "v1.0.0", 0},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
