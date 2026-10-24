package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestFetchGeminiModelsPagesFiltersAndProtectsKey(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/gateway/v1beta/models" || r.Header.Get("x-goog-api-key") != "synthetic-gemini-key" || strings.Contains(r.URL.RawQuery, "synthetic") {
			t.Error("incorrect upstream authentication or path")
		}
		if r.URL.Query().Get("pageSize") != "1000" {
			t.Error("missing page size")
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-pro","displayName":"Gemini Pro","supportedGenerationMethods":["generateContent"]},{"name":"models/embedding","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"page two"}`))
		} else {
			if r.URL.Query().Get("pageToken") != "page two" {
				t.Error("missing page token")
			}
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-pro"},{"name":"models/gemini-flash"},{"name":"--invalid"}]}`))
		}
	}))
	defer upstream.Close()
	models, err := fetchGeminiModels(context.Background(), upstream.URL+"/gateway/v1beta/", "synthetic-gemini-key")
	if err != nil || len(models) != 2 || models[0].ID != "gemini-flash" || models[1].ID != "gemini-pro" || models[1].Name != "Gemini Pro" || calls != 2 {
		t.Fatalf("unexpected models: %+v %v calls=%d", models, err, calls)
	}
}

func TestFetchGeminiModelsRejectsRedirectAndRedactsErrors(t *testing.T) {
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++ }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/?key=synthetic-secret", http.StatusFound)
	}))
	defer upstream.Close()
	_, err := fetchGeminiModels(context.Background(), upstream.URL, "synthetic-gemini-key")
	if err == nil || strings.Contains(err.Error(), "synthetic") || redirected != 0 {
		t.Fatalf("redirect or credential exposure: %v", err)
	}
	for _, body := range []string{`not json synthetic-secret`, `{"models":[],"nextPageToken":"repeat"}`} {
		t.Run(body, func(t *testing.T) {
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer bad.Close()
			_, err := fetchGeminiModels(context.Background(), bad.URL, "synthetic-gemini-key")
			if err == nil || strings.Contains(err.Error(), "synthetic") {
				t.Fatalf("bad error: %v", err)
			}
		})
	}
}

func TestWorkspaceGeminiModelsUsesBoundProfileAndHandlesMissingConfig(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "gemini-models-token")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/models", Name: "models"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatal("missing workspace", err)
	}
	workspace := workspaces[0]
	a := resourceTestAPI(db)
	path := "/api/workspaces/" + workspace.ID + "/gemini/models"
	request := func() *httptest.ResponseRecorder {
		return workspaceResourceRequest(t, http.MethodGet, path, workspace.ID, nil, a.workspaceGeminiModels)
	}
	result := request()
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"configured":false`) {
		t.Fatal(result.Code, result.Body.String())
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "synthetic-gemini-key" {
			t.Error("wrong profile key")
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-upstream","supportedGenerationMethods":["generateContent"]}]}`))
	}))
	defer upstream.Close()
	encrypted, _ := a.vault.Encrypt("synthetic-gemini-key")
	profile, err := db.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "gemini", Name: "Upstream", Endpoint: upstream.URL, Model: "auto"}, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO server_gemini_credential_profiles(server_id,profile_id) VALUES(?,?)"), server.ID, profile.ID); err != nil {
		t.Fatal(err)
	}
	result = request()
	var body struct {
		Models     []geminiModel `json:"models"`
		Configured bool          `json:"configured"`
	}
	if json.Unmarshal(result.Body.Bytes(), &body) != nil || result.Code != 200 || !body.Configured || len(body.Models) != 1 || body.Models[0].ID != "gemini-upstream" || strings.Contains(result.Body.String(), "synthetic") {
		t.Fatal(result.Code, result.Body.String())
	}
	if result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("model list must refresh from upstream")
	}
	missing := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/missing/gemini/models", "missing", nil, a.workspaceGeminiModels)
	if missing.Code != 404 {
		t.Fatal("missing workspace accepted")
	}
}
