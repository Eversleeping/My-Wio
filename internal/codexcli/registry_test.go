package codexcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestOversizedGitHubResponseFallsBackToOfficialPackageAndCaches(t *testing.T) {
	githubCalls, npmCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/github" {
			githubCalls++
			w.Write([]byte(strings.Repeat("x", githubReleaseMaxBytes+1)))
			return
		}
		npmCalls++
		if r.Header.Get("Accept") != "application/vnd.npm.install-v1+json" {
			t.Error("missing abbreviated npm metadata header")
		}
		w.Write([]byte(`{"versions":{"0.64.0":{},"0.65.0":{},"0.66.0-preview.1":{},"0.63.0":{}}}`))
	}))
	defer server.Close()
	checker := NewTaggedReleaseChecker(server.Client(), server.URL+"/github", "v")
	checker.registryURL = server.URL + "/npm"
	for i := 0; i < 2; i++ {
		versions, err := checker.RecentStable(context.Background(), 5)
		if err != nil || !reflect.DeepEqual(versions, []string{"0.65.0", "0.64.0", "0.63.0"}) {
			t.Fatalf("versions=%v err=%v", versions, err)
		}
	}
	if githubCalls != 1 || npmCalls != 1 {
		t.Fatalf("uncached release requests github=%d npm=%d", githubCalls, npmCalls)
	}
}
