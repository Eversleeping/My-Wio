package gitprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateProviderRepositories(t *testing.T) {
	for _, test := range []struct {
		name       string
		provider   string
		namespace  string
		wantPath   string
		response   string
		prepare    func(http.ResponseWriter, *http.Request) bool
		wantRemote string
	}{
		{name: "gitee organization", provider: "gitee", namespace: "team", wantPath: "/api/v5/orgs/team/repos", response: `{"html_url":"https://gitee.example/team/demo","clone_url":"https://gitee.example/team/demo.git"}`, wantRemote: "https://gitee.example/team/demo.git"},
		{name: "github user", provider: "github", wantPath: "/api/v3/user/repos", response: `{"html_url":"https://github.example/user/demo","clone_url":"https://github.example/user/demo.git"}`, wantRemote: "https://github.example/user/demo.git"},
		{name: "gitlab namespace", provider: "gitlab", namespace: "group/subgroup", wantPath: "/api/v4/projects", response: `{"web_url":"https://gitlab.example/group/subgroup/demo","http_url_to_repo":"https://gitlab.example/group/subgroup/demo.git","path":"demo"}`, wantRemote: "https://gitlab.example/group/subgroup/demo.git", prepare: func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/api/v4/namespaces" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"id":42,"path":"subgroup","full_path":"group/subgroup"}]`))
				return true
			}
			return false
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var serverURL string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.prepare != nil && test.prepare(w, r) {
					return
				}
				if r.URL.Path != test.wantPath {
					t.Fatalf("unexpected provider path: %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer provider-token" {
					t.Fatalf("provider token was not sent in the authorization header")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(mustJSON(body), "provider-token") || body["description"] != markerPrefix+"project-1" {
					t.Fatalf("unexpected provider body: %#v", body)
				}
				if test.provider == "gitlab" && body["namespace_id"] != float64(42) {
					t.Fatalf("GitLab namespace was not resolved: %#v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()
			serverURL = server.URL
			result, err := (Client{HTTPClient: server.Client()}).Create(context.Background(), Request{ProjectID: "project-1", Provider: test.provider, Endpoint: serverURL, Token: "provider-token", Namespace: test.namespace, Repository: "demo", Visibility: "private"})
			if err != nil {
				t.Fatal(err)
			}
			if result.FetchURL != test.wantRemote || result.PushURL != test.wantRemote || result.Provider != test.provider {
				t.Fatalf("unexpected provider result: %#v", result)
			}
		})
	}
}

func TestCreateProviderRepositoryRecoversMarkedConflict(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/user/repos":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"name already exists"}`))
		case "/api/v3/user":
			_, _ = w.Write([]byte(`{"login":"owner"}`))
		case "/api/v3/repos/owner/demo":
			_, _ = w.Write([]byte(`{"description":"wio-project:project-2","html_url":"https://git.example/owner/demo","clone_url":"https://git.example/owner/demo.git"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result, err := (Client{HTTPClient: server.Client()}).Create(context.Background(), Request{ProjectID: "project-2", Provider: "github", Endpoint: server.URL, Token: "provider-token", Repository: "demo", Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FetchURL != "https://git.example/owner/demo.git" || result.Namespace != "owner" {
		t.Fatalf("unexpected recovered repository: %#v", result)
	}
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
