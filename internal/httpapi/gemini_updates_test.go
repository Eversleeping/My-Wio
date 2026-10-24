package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/store"
)

func TestCheckGeminiCLIUpdatesSavesLatestStableRelease(t *testing.T) {
	database := openBootstrapTestStore(t)
	if err := database.SetSetting(context.Background(), geminiCLITargetSetting, "0.144.4"); err != nil {
		t.Fatal(err)
	}
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.145.0-alpha.1","prerelease":false},
			{"tag_name":"v0.144.5","prerelease":false}
		]`))
	}))
	defer releases.Close()
	api := resourceTestAPI(database)
	api.geminiReleases = codexcli.NewTaggedReleaseChecker(releases.Client(), releases.URL, "v")
	request := httptest.NewRequest(http.MethodPost, "/api/settings/gemini-cli/check-updates", nil)
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, store.Session{UserID: "test-user"}))
	response := httptest.NewRecorder()
	api.checkGeminiCLIUpdates(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("release check returned %d: %s", response.Code, response.Body.String())
	}
	var result geminiCLISettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.TargetVersion != "0.144.5" || result.LatestVersion != "0.144.5" || !result.Updated || len(result.Versions) != 1 {
		t.Fatalf("unexpected release check result: %#v %v", result, err)
	}
	stored, err := database.Setting(context.Background(), geminiCLITargetSetting, "")
	if err != nil || stored != "0.144.5" {
		t.Fatalf("latest release was not saved: %q %v", stored, err)
	}
}

func TestSelectGeminiCLIVersionRequiresRecentFormalRelease(t *testing.T) {
	database := openBootstrapTestStore(t)
	if err := database.SetSetting(context.Background(), geminiCLITargetSetting, "0.144.5"); err != nil {
		t.Fatal(err)
	}
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.144.5"},
			{"tag_name":"v0.144.4"},
			{"tag_name":"v0.144.3"},
			{"tag_name":"v0.144.2"},
			{"tag_name":"v0.144.1"}
		]`))
	}))
	defer releases.Close()
	api := resourceTestAPI(database)
	api.geminiReleases = codexcli.NewTaggedReleaseChecker(releases.Client(), releases.URL, "v")

	request := httptest.NewRequest(http.MethodPost, "/api/settings/gemini-cli/select-version", strings.NewReader(`{"version":"0.140.0"}`))
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, store.Session{UserID: "test-user"}))
	response := httptest.NewRecorder()
	api.selectGeminiCLIVersion(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected invalid selection status: %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/settings/gemini-cli/select-version", strings.NewReader(`{"version":"0.144.3"}`))
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, store.Session{UserID: "test-user"}))
	response = httptest.NewRecorder()
	api.selectGeminiCLIVersion(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected valid selection status: %d: %s", response.Code, response.Body.String())
	}
	stored, err := database.Setting(context.Background(), geminiCLITargetSetting, "")
	if err != nil || stored != "0.144.3" {
		t.Fatalf("selected release was not saved: %q %v", stored, err)
	}
}
