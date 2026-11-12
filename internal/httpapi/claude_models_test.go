package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestFetchClaudeModelsPagesAndProtectsKey(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.EscapedPath() != "/gateway%20api/v1/models" || r.Header.Get("x-api-key") != "synthetic-claude-key" || r.Header.Get("anthropic-version") != "2023-06-01" || strings.Contains(r.URL.RawQuery, "synthetic") {
			t.Error("incorrect upstream path or authentication")
		}
		if r.URL.Query().Get("limit") != "1000" {
			t.Error("missing page size")
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-test","display_name":"Sonnet test"},{"id":"--invalid"}],"has_more":true,"last_id":"cursor one"}`))
		} else {
			if r.URL.Query().Get("after_id") != "cursor one" {
				t.Error("missing pagination cursor")
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-test"},{"id":"custom-model"}],"has_more":false}`))
		}
	}))
	defer upstream.Close()
	models, err := fetchClaudeModels(context.Background(), upstream.URL+"/gateway%20api/", "synthetic-claude-key")
	if err != nil || calls != 2 || len(models) != 2 || models[0].ID != "claude-sonnet-test" || models[0].Name != "Sonnet test" || models[1].ID != "custom-model" {
		t.Fatalf("unexpected models: %+v %v calls=%d", models, err, calls)
	}
}

func TestFetchClaudeModelsRejectsRedirectAndRedactsErrors(t *testing.T) {
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++ }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/?key=synthetic-secret", http.StatusFound)
	}))
	defer upstream.Close()
	_, err := fetchClaudeModels(context.Background(), upstream.URL, "synthetic-key")
	if err == nil || strings.Contains(err.Error(), "synthetic") || redirected != 0 {
		t.Fatalf("redirect or credential exposure: %v", err)
	}
	for _, body := range []string{`not json synthetic-secret`, `{}`, `{"data":[],"has_more":true}`, `{"data":[],"has_more":true,"last_id":"repeat"}`} {
		t.Run(body, func(t *testing.T) {
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer bad.Close()
			_, err := fetchClaudeModels(context.Background(), bad.URL, "synthetic-key")
			if err == nil || strings.Contains(err.Error(), "synthetic") {
				t.Fatalf("bad upstream error: %v", err)
			}
		})
	}
}

func TestWorkspaceClaudeModelsUsesAppliedCredentials(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "claude-models-token")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/models", Name: "models"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatal("missing workspace", err)
	}
	a := resourceTestAPI(db)
	path := "/api/workspaces/" + workspaces[0].ID + "/claude/models"
	request := func() *httptest.ResponseRecorder {
		return workspaceResourceRequest(t, http.MethodGet, path, workspaces[0].ID, nil, a.workspaceClaudeModels)
	}
	if result := request(); result.Code != 200 || !strings.Contains(result.Body.String(), `"configured":false`) {
		t.Fatal(result.Code, result.Body.String())
	}
	var changeDuringRequest func()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "synthetic-claude-key" {
			t.Error("wrong profile key")
		}
		if changeDuringRequest != nil {
			changeDuringRequest()
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"custom-model","display_name":"Custom Model"}]}`))
	}))
	defer upstream.Close()
	encrypted, _ := a.vault.Encrypt("synthetic-claude-key")
	profile, err := db.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "claude", Name: "Upstream", Endpoint: upstream.URL, Model: "sonnet"}, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	apply := func() {
		t.Helper()
		payload, err := a.vault.Encrypt(protocol.ConfigureClaudeCredentialsCommand{Endpoint: profile.Endpoint, APIKey: "synthetic-claude-key", Model: profile.Model})
		if err != nil {
			t.Fatal(err)
		}
		op, err := db.QueueClaudeCredentialUpdate(ctx, server.ID, profile.ID, payload, profile.Model)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CompleteClaudeCredentialUpdate(ctx, protocol.OperationResult{OperationID: op, Status: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	result := request()
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"id":"custom-model"`) || strings.Contains(result.Body.String(), "synthetic") || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(result.Code, result.Body.String())
	}
	profile.Model = "custom-model"
	if _, err := db.SaveCredentialProfile(ctx, profile, encrypted); err != nil {
		t.Fatal(err)
	}
	if result = request(); result.Code != 200 || !strings.Contains(result.Body.String(), `"default_model":"custom-model"`) || !strings.Contains(result.Body.String(), `"credential_revision":`) {
		t.Fatal("model-only edit required reapplying credentials", result.Code, result.Body.String())
	}
	// An endpoint or key edit still requires a successful credential apply.
	originalEndpoint := profile.Endpoint
	profile.Endpoint += "/changed"
	if _, err := db.SaveCredentialProfile(ctx, profile, ""); err != nil {
		t.Fatal(err)
	}
	if result = request(); result.Code != 409 || !strings.Contains(result.Body.String(), "reapply") {
		t.Fatal("unapplied endpoint was used", result.Code, result.Body.String())
	}
	profile.Endpoint = originalEndpoint
	rotatedKey, _ := a.vault.Encrypt("synthetic-rotated-key")
	if _, err := db.SaveCredentialProfile(ctx, profile, rotatedKey); err != nil {
		t.Fatal(err)
	}
	if result = request(); result.Code != 409 || strings.Contains(result.Body.String(), "synthetic") {
		t.Fatal("unapplied key was used", result.Code, result.Body.String())
	}
	if _, err := db.SaveCredentialProfile(ctx, profile, encrypted); err != nil {
		t.Fatal(err)
	}
	apply()
	if result = request(); result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	changeDuringRequest = func() {
		profile.Model = "another-model"
		if _, err := db.SaveCredentialProfile(ctx, profile, encrypted); err != nil {
			t.Error(err)
		}
	}
	if result = request(); result.Code != 409 || strings.Contains(result.Body.String(), "synthetic") {
		t.Fatal("in-flight preset edit not detected", result.Code, result.Body.String())
	}
	missing := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/missing/claude/models", "missing", nil, a.workspaceClaudeModels)
	if missing.Code != 404 {
		t.Fatal("missing workspace accepted")
	}
}
