package codexcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestRecentStableReturnsFiveNewestFormalVersionsAcrossPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`[
				{"tag_name":"rust-v0.145.0-alpha.1","prerelease":false},
				{"tag_name":"rust-v0.144.5","prerelease":false},
				{"tag_name":"rust-v0.144.4","prerelease":false},
				{"tag_name":"rust-v0.144.3","prerelease":false},
				{"tag_name":"rust-v0.144.2","prerelease":false},
				{"tag_name":"rust-v0.144.1","prerelease":false},
				{"tag_name":"v9.9.9","prerelease":false},
				{"tag_name":"rust-v0.144.0-rc.1","prerelease":false},
				{"tag_name":"rust-v0.143.9","prerelease":true},
				{"tag_name":"rust-v0.143.8","draft":true}
			]`))
			return
		}
		t.Fatalf("unexpected page request after enough stable releases: %s", r.URL.Query().Get("page"))
	}))
	defer server.Close()

	versions, err := NewReleaseChecker(server.Client(), server.URL).RecentStable(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"0.144.5", "0.144.4", "0.144.3", "0.144.2", "0.144.1"}
	if !reflect.DeepEqual(versions, expected) {
		t.Fatalf("unexpected recent stable versions: %#v", versions)
	}
}

func TestRecentStableContinuesPaginationWhenPreviewReleasesFillPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`[
				{"tag_name":"rust-v0.145.0-alpha.1","prerelease":false},
				{"tag_name":"rust-v0.144.6-rc.1","prerelease":false},
				{"tag_name":"rust-v0.144.5","prerelease":true},
				{"tag_name":"rust-v0.144.4","draft":true},
				{"tag_name":"rust-v0.144.3-beta.1","prerelease":false},
				{"tag_name":"v9.9.9"},
				{"tag_name":"rust-v0.144.2-alpha.1"},
				{"tag_name":"rust-v0.144.1-rc.1"},
				{"tag_name":"rust-v0.144.0"},
				{"tag_name":"rust-v0.143.9"}
			]`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"tag_name":"rust-v0.143.8"},
			{"tag_name":"rust-v0.143.7"}
		]`))
	}))
	defer server.Close()

	versions, err := NewReleaseChecker(server.Client(), server.URL).RecentStable(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if expected := []string{"0.144.0", "0.143.9", "0.143.8"}; !reflect.DeepEqual(versions, expected) {
		t.Fatalf("unexpected paginated stable versions: %#v", versions)
	}
}
