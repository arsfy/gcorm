package versioncheck

import (
	"context"
	"net/http"
	"net/http/httptest"
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
