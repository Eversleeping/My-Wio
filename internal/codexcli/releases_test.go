package codexcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestStableReleaseExcludesPreviewTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("User-Agent") != "Wio-Controlplane" {
			t.Fatalf("missing GitHub API headers: %#v", r.Header)
		}
		if r.URL.Query().Get("per_page") != "10" || r.URL.Query().Get("page") != "1" {
			t.Fatalf("unexpected release pagination: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name":"rust-v0.145.0-alpha.2","draft":false,"prerelease":false},
			{"tag_name":"rust-v0.144.6","draft":false,"prerelease":true},
			{"tag_name":"rust-v0.144.5","draft":false,"prerelease":false},
			{"tag_name":"rust-v0.144.4","draft":false,"prerelease":false},
			{"tag_name":"v9.9.9","draft":false,"prerelease":false}
		]`))
	}))
	defer server.Close()

	version, err := NewReleaseChecker(server.Client(), server.URL).LatestStable(context.Background())
	if err != nil || version != "0.144.5" {
		t.Fatalf("unexpected latest release: %q %v", version, err)
	}
}

func TestLatestStableReleaseRequiresFormalRustTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`[{"tag_name":"rust-v0.145.0-alpha.1"},{"tag_name":"rust-v0.144.5-rc.1"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	if _, err := NewReleaseChecker(server.Client(), server.URL).LatestStable(context.Background()); err != ErrNoStableRelease {
		t.Fatalf("expected no stable release, got %v", err)
	}
}
