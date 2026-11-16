package credentialproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const nativeGoogleProject = "synthetic-native-project"

func TestNativeGoogleLeaseCannotChooseAnotherProjectOrAccount(t *testing.T) {
	var reached atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	b, err := Start(Config{Provider: "gemini", Native: true, Project: nativeGoogleProject, Upstream: upstream.URL, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.stop()
	for _, attack := range []struct{ label, path, body string }{
		{"foreign-inference-project", "/v1internal:streamGenerateContent", `{"project":"foreign-project","model":"gemini-2.5-flash","request":{"contents":[]}}`},
		{"foreign-load-project", "/v1internal:loadCodeAssist", `{"cloudaicompanionProject":"foreign-project","metadata":{"duetProject":"synthetic-native-project"}}`},
		{"foreign-metadata-project", "/v1internal:loadCodeAssist", `{"cloudaicompanionProject":"synthetic-native-project","metadata":{"duetProject":"foreign-project"}}`},
		{"foreign-quota-project", "/v1internal:retrieveUserQuota", `{"project":"foreign-project"}`},
		{"case-ambiguous-project", "/v1internal:streamGenerateContent", `{"project":"synthetic-native-project","Project":"foreign-project","request":{"contents":[]}}`},
		{"snake-case-load-project", "/v1internal:loadCodeAssist", `{"cloudaicompanionProject":"synthetic-native-project","cloudaicompanion_project":"foreign-project"}`},
		{"project-id-alias", "/v1internal:streamGenerateContent", `{"project":"synthetic-native-project","projectId":"foreign-project","request":{"contents":[]}}`},
		{"foreign-model-resource", "/v1internal:streamGenerateContent", `{"project":"synthetic-native-project","model":"projects/foreign-project/locations/global/publishers/google/models/gemini-2.5-flash","request":{"contents":[]}}`},
		{"foreign-model-alias", "/v1internal:streamGenerateContent", `{"project":"synthetic-native-project","model_name":"projects/foreign-project/locations/global/publishers/google/models/gemini-2.5-flash","request":{"contents":[]}}`},
		{"foreign-count-model-resource", "/v1internal:countTokens", `{"request":{"model":"projects/foreign-project/locations/global/publishers/google/models/gemini-2.5-flash","contents":[]}}`},
		{"foreign-cached-content", "/v1internal:streamGenerateContent", `{"project":"synthetic-native-project","request":{"contents":[],"cachedContent":"projects/foreign-project/locations/global/cachedContents/foreign-content"}}`},
		{"cached-content-traversal", "/v1internal:streamGenerateContent", `{"project":"synthetic-native-project","request":{"cachedContent":"projects/synthetic-native-project/../foreign-project/locations/global/cachedContents/foreign-content"}}`},
		{"cached-content-encoded-traversal", "/v1internal:streamGenerateContent", `{"project":"synthetic-native-project","request":{"cachedContent":"projects/synthetic-native-project/locations/%2e%2e/%2e%2e/foreign-project/cachedContents/foreign-content"}}`},
		{"cached-content-snake-alias", "/v1internal:streamGenerateContent", `{"project":"synthetic-native-project","request":{"cached_content":"projects/foreign-project/locations/global/cachedContents/foreign-content"}}`},
		{"account-management", "/v1internal:setCodeAssistGlobalUserSetting", `{}`},
		{"onboard-user", "/v1internal:onboardUser", `{}`},
		{"token-exchange", "/oauth2/token", `{"refresh_token":"synthetic-session-refresh"}`},
	} {
		t.Run(attack.label, func(t *testing.T) {
			before := reached.Load()
			r, _ := http.NewRequest(http.MethodPost, "http://"+b.address+attack.path, strings.NewReader(attack.body))
			r.Header.Set("Authorization", "Bearer "+b.token)
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode < 400 || reached.Load() != before {
				t.Fatalf("untrusted project/account request reached the fixed upstream: status=%d", response.StatusCode)
			}
		})
	}
}

func TestNativeGoogleProjectBindingPreservesMessageTextAndFixedAccount(t *testing.T) {
	message := `{"project":"foreign-project","cachedContent":"projects/foreign-project/locations/global/cachedContents/fake","model":"projects/foreign-project/models/fake"}`
	var reached atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+syntheticSecret || r.Header.Get("x-goog-user-project") != "" || r.Header.Get("x-goog-api-key") != "" {
			t.Error("untrusted account or quota headers forwarded")
		}
		var payload map[string]any
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["project"] != nativeGoogleProject {
			t.Error("missing trusted project binding")
		}
		request, _ := payload["request"].(map[string]any)
		contents, _ := request["contents"].([]any)
		if len(contents) != 1 {
			t.Error("message content changed")
			return
		}
		content, _ := contents[0].(map[string]any)
		parts, _ := content["parts"].([]any)
		if len(parts) != 1 {
			t.Error("message parts changed")
			return
		}
		part, _ := parts[0].(map[string]any)
		if part["text"] != message {
			t.Error("ordinary user JSON text was treated as a scope instruction")
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	b, err := Start(Config{Provider: "gemini", Native: true, Project: nativeGoogleProject, Upstream: upstream.URL, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.stop()
	body, _ := json.Marshal(map[string]any{"model": "gemini-2.5-flash", "request": map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": message}}}}}})
	r, _ := http.NewRequest(http.MethodPost, "http://"+b.address+"/v1internal:streamGenerateContent", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+b.token)
	r.Header.Set("x-goog-user-project", "foreign-project")
	r.Header.Set("x-goog-api-key", "foreign-account-secret")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || reached.Load() != 1 {
		t.Fatal("ordinary native request rejected")
	}
	for _, bearer := range []string{"", syntheticSecret, "foreign-account-secret"} {
		r, _ := http.NewRequest(http.MethodPost, "http://"+b.address+"/v1internal:streamGenerateContent", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+bearer)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden || reached.Load() != 1 {
			t.Error("upstream or foreign account bearer bypassed the temporary lease")
		}
	}
}
