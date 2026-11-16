package sessionisolation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/credentialproxy"
)

func syntheticJWT(expiry time.Time) string {
	data, _ := json.Marshal(map[string]any{"exp": expiry.Unix()})
	return "synthetic." + base64.RawURLEncoding.EncodeToString(data) + ".signature"
}

func writeConfiguration(t *testing.T, path string, value any) []byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCLIConfigurationContainsOnlyRevocableAuthentication(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("configuration preparation must not call the model")
	}))
	defer upstream.Close()
	for _, provider := range []string{"codex", "claude", "gemini"} {
		for _, native := range []bool{false, true} {
			name := provider + "/api"
			if native {
				name = provider + "/native"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				state := filepath.Join(home, "."+provider)
				env := environment{"HOME": home}
				access, refresh := "synthetic-upstream-access", "synthetic-upstream-refresh"
				expires := time.Now().Add(time.Hour)
				if provider == "codex" && native {
					access = syntheticJWT(expires)
				}
				original := map[string][]byte{}
				write := func(file string, value any) {
					path := filepath.Join(state, file)
					original[path] = writeConfiguration(t, path, value)
				}
				switch provider {
				case "codex":
					if err := os.MkdirAll(state, 0700); err != nil {
						t.Fatal(err)
					}
					settings := "model = 'test-model'\nmodel_provider = 'test'\n[model_providers.test]\nbase_url = '" + upstream.URL + "'\nwire_api = 'responses'\n"
					if native {
						settings = "model = 'test-model'\nopenai_base_url = '" + upstream.URL + "'\n"
						write("auth.json", map[string]any{"tokens": map[string]any{"access_token": access, "id_token": access, "refresh_token": refresh, "account_id": "synthetic-account"}})
					} else {
						env["WIO_CODEX_API_KEY"] = access
					}
					path := filepath.Join(state, "config.toml")
					if err := os.WriteFile(path, []byte(settings), 0600); err != nil {
						t.Fatal(err)
					}
					original[path] = []byte(settings)
				case "claude":
					env["ANTHROPIC_BASE_URL"] = upstream.URL
					write("settings.json", map[string]any{"model": "test-model", "env": map[string]any{"ANTHROPIC_API_KEY": access}, "apiKeyHelper": "untrusted-key-helper"})
					if native {
						write(".credentials.json", map[string]any{"claudeAiOauth": map[string]any{"accessToken": access, "refreshToken": refresh, "expiresAt": expires.UnixMilli()}})
					} else {
						env["ANTHROPIC_API_KEY"] = access
					}
				case "gemini":
					env["GOOGLE_GEMINI_BASE_URL"], env["CODE_ASSIST_ENDPOINT"] = upstream.URL, upstream.URL
					write("settings.json", map[string]any{"ui": map[string]any{"theme": "Default"}})
					if native {
						env["GOOGLE_CLOUD_PROJECT"] = "synthetic-project"
						write("oauth_creds.json", map[string]any{"access_token": access, "refresh_token": refresh, "expiry_date": expires.UnixMilli()})
					} else {
						env["GEMINI_API_KEY"] = access
					}
				}
				auth, err := parseAuthentication(provider, env, &exec.Cmd{})
				if err != nil || auth.config.Native != native {
					t.Fatalf("supported authentication was not resolved: %v", err)
				}
				lease, err := credentialproxy.Start(auth.config)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				public := publicEnvironment(env, provider)
				files, err := auth.leaseConfiguration(lease, public)
				if err != nil {
					t.Fatal(err)
				}
				combined := strings.Join(public.entries(), "\n")
				for _, file := range files {
					combined += string(file)
				}
				if strings.Contains(combined, access) || strings.Contains(combined, refresh) || !strings.Contains(combined, lease.Token()) || !strings.Contains(combined, lease.Endpoint()) {
					t.Fatal("private configuration retained upstream authentication or omitted its lease")
				}
				for path, before := range original {
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("trusted account configuration was modified")
					}
				}
			})
		}
	}
}

func TestIsolationRefusesUnsupportedCloudCredentials(t *testing.T) {
	for _, input := range []struct {
		provider string
		env      environment
	}{
		{"claude", environment{"CLAUDE_CODE_USE_BEDROCK": "true"}},
		{"claude", environment{"CLAUDE_CODE_USE_VERTEX": "1"}},
		{"claude", environment{"CLAUDE_CODE_USE_FOUNDRY": "true"}},
		{"claude", environment{"ANTHROPIC_CUSTOM_HEADERS": "Authorization: synthetic-secret"}},
		{"gemini", environment{"GOOGLE_GENAI_USE_VERTEXAI": "true"}},
		{"gemini", environment{"GOOGLE_APPLICATION_CREDENTIALS": "/synthetic/credentials.json"}},
		{"codex", environment{"OPENAI_API_KEY": ""}},
	} {
		input.env["HOME"] = t.TempDir()
		if _, err := parseAuthentication(input.provider, input.env, &exec.Cmd{}); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("unsupported authentication was accepted or exposed")
		}
	}
}

func TestGeminiJSONCommentsAndModelAliasInputsSurviveIsolation(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	settings := []byte("{\n// user comment\n\"modelConfigs\": {\"customAliases\": {\"chosen\": {\"modelConfig\": {\"model\": \"${WIO_GEMINI_THINKING_MODEL}\"}}}},\n}\n")
	if err := os.WriteFile(path, settings, 0600); err != nil {
		t.Fatal(err)
	}
	env := environment{"HOME": home, "GEMINI_API_KEY": "synthetic-upstream", "WIO_GEMINI_THINKING_MODEL": "gemini-3-flash-preview", "WIO_GEMINI_THINKING_LEVEL": "LOW", "WIO_GEMINI_LOOP_MODEL": "gemini-3-flash-preview", "WIO_GEMINI_UNREVIEWED_SECRET": "private", "CLAUDE_CODE_MAX_OUTPUT_TOKENS": "1024"}
	auth, err := parseAuthentication("gemini", env, &exec.Cmd{})
	if err != nil || len(object(auth.settings["modelConfigs"])) == 0 {
		t.Fatalf("supported Gemini settings failed: %v", err)
	}
	public := publicEnvironment(env, "gemini")
	for _, name := range []string{"WIO_GEMINI_THINKING_MODEL", "WIO_GEMINI_THINKING_LEVEL", "WIO_GEMINI_LOOP_MODEL"} {
		if public[name] != env[name] {
			t.Fatal("Gemini model choice changed in isolation")
		}
		if publicEnvironment(env, "codex")[name] != "" {
			t.Fatal("Gemini model setting reached another engine")
		}
	}
	if public["WIO_GEMINI_UNREVIEWED_SECRET"] != "" || public["GEMINI_API_KEY"] != "" {
		t.Fatal("unreviewed authentication reached the public environment")
	}
	if publicEnvironment(env, "claude")["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != "1024" {
		t.Fatal("Claude output token budget was removed with authentication")
	}
	limits := object(sanitizedSettings(map[string]any{"maxOutputTokens": 1024, "model_max_output_tokens": 2048, "accessToken": "synthetic-private"}))
	if len(limits) != 2 || limits["maxOutputTokens"] != 1024 || limits["model_max_output_tokens"] != 2048 {
		t.Fatal("public model budgets were removed from CLI settings")
	}
}
