package sessionisolation

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"github.com/wio-platform/wio/internal/credentialproxy"
)

const claudeKeyDescriptor = "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR"

type authentication struct {
	config     credentialproxy.Config
	home       string
	state      string
	settings   map[string]any
	account    map[string]any
	descriptor int
}

func readObject(path string, useTOML bool) (map[string]any, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, errors.New("cannot read trusted CLI configuration")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return nil, errors.New("trusted CLI configuration is too large")
	}
	out := map[string]any{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return out, nil
	}
	if useTOML {
		err = toml.Unmarshal(data, &out)
	} else {
		// Gemini accepts comments and trailing commas in account settings.
		// Keep that supported syntax when preparing the private configuration.
		var normalized []byte
		normalized, err = hujson.Standardize(data)
		if err == nil {
			err = json.Unmarshal(normalized, &out)
		}
	}
	if err != nil {
		return nil, errors.New("invalid trusted CLI configuration")
	}
	return out, nil
}

func object(value any) map[string]any {
	out, _ := value.(map[string]any)
	if out == nil {
		return map[string]any{}
	}
	return out
}

func stringValue(value any) string {
	out, _ := value.(string)
	return out
}

func milliseconds(value any) time.Time {
	var number int64
	switch value := value.(type) {
	case float64:
		number = int64(value)
	case int64:
		number = value
	}
	if number <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(number)
}

func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(data) > 32<<10 {
		return time.Time{}
	}
	var payload struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(payload.Exp, 0)
}

func parseAuthentication(provider string, env environment, cmd *exec.Cmd) (authentication, error) {
	a := authentication{config: credentialproxy.Config{Provider: provider}, home: env["HOME"], descriptor: -1}
	if !validPath(a.home) {
		return a, errors.New("session isolation requires an absolute CLI home")
	}
	var err error
	key := ""
	switch provider {
	case "codex":
		a.state = env["CODEX_HOME"]
		if a.state == "" {
			a.state = filepath.Join(a.home, ".codex")
		}
		if a.settings, err = readObject(filepath.Join(a.state, "config.toml"), true); err != nil {
			return a, err
		}
		providerName := stringValue(a.settings["model_provider"])
		selected := object(object(a.settings["model_providers"])[providerName])
		if wire := stringValue(selected["wire_api"]); wire != "" && wire != "responses" {
			return a, errors.New("this Codex model protocol is unsupported in session isolation")
		}
		if len(object(selected["http_headers"])) != 0 || len(object(selected["env_http_headers"])) != 0 {
			return a, errors.New("custom Codex provider headers are unsupported in session isolation")
		}
		a.config.Upstream = stringValue(selected["base_url"])
		key = env.first("WIO_CODEX_API_KEY", stringValue(selected["env_key"]), "OPENAI_API_KEY", "CODEX_API_KEY")
		if key == "" {
			key = stringValue(selected["experimental_bearer_token"])
		}
		if key != "" {
			if a.config.Upstream == "" {
				a.config.Upstream = env.first("OPENAI_BASE_URL", "OPENAI_API_BASE")
			}
			if a.config.Upstream == "" {
				a.config.Upstream = "https://api.openai.com/v1"
			}
		} else {
			login, err := readObject(filepath.Join(a.state, "auth.json"), false)
			if err != nil {
				return a, err
			}
			if key = stringValue(login["OPENAI_API_KEY"]); key != "" {
				if a.config.Upstream == "" {
					a.config.Upstream = "https://api.openai.com/v1"
				}
			} else {
				tokens := object(login["tokens"])
				key = stringValue(tokens["access_token"])
				a.config.Native = true
				a.config.AccountID = stringValue(tokens["account_id"])
				a.config.ExpiresAt = tokenExpiry(key)
				if a.config.ExpiresAt.IsZero() {
					a.config.ExpiresAt = tokenExpiry(stringValue(tokens["id_token"]))
				}
				a.config.Secrets = append(a.config.Secrets, stringValue(tokens["refresh_token"]), stringValue(tokens["id_token"]))
				a.config.Upstream = stringValue(a.settings["openai_base_url"])
				if a.config.Upstream == "" {
					a.config.Upstream = "https://chatgpt.com/backend-api/codex"
				}
			}
		}
		a.config.Credentials = []credentialproxy.Credential{{Header: "Authorization", Value: "Bearer " + key}}
	case "claude":
		for _, name := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
			if isTrue(env[name]) {
				return a, errors.New("this Claude cloud authentication is unsupported in session isolation")
			}
		}
		if env["ANTHROPIC_CUSTOM_HEADERS"] != "" {
			return a, errors.New("custom Claude authentication headers are unsupported in session isolation")
		}
		a.state = env["CLAUDE_CONFIG_DIR"]
		if a.state == "" {
			a.state = filepath.Join(a.home, ".claude")
		}
		if a.settings, err = readObject(filepath.Join(a.state, "settings.json"), false); err != nil {
			return a, err
		}
		a.config.Upstream = env["ANTHROPIC_BASE_URL"]
		if a.config.Upstream == "" {
			a.config.Upstream = "https://api.anthropic.com"
		}
		key = env["ANTHROPIC_API_KEY"]
		if descriptor := env[claudeKeyDescriptor]; descriptor != "" {
			fd, err := strconv.Atoi(descriptor)
			if err != nil || fd < 3 || fd-3 >= len(cmd.ExtraFiles) || cmd.ExtraFiles[fd-3] == nil {
				return a, errors.New("invalid Claude API descriptor")
			}
			data, err := io.ReadAll(io.LimitReader(cmd.ExtraFiles[fd-3], (32<<10)+1))
			if err != nil || len(data) > 32<<10 {
				return a, errors.New("cannot read trusted Claude API descriptor")
			}
			key, a.descriptor = strings.TrimSpace(string(data)), fd-3
		}
		header := "x-api-key"
		if key == "" && env["ANTHROPIC_AUTH_TOKEN"] != "" {
			key, header = env["ANTHROPIC_AUTH_TOKEN"], "Authorization"
		}
		if key == "" {
			a.config.Native, header = true, "Authorization"
			key = env["CLAUDE_CODE_OAUTH_TOKEN"]
			login, err := readObject(filepath.Join(a.state, ".credentials.json"), false)
			if err != nil {
				return a, err
			}
			oauth := object(login["claudeAiOauth"])
			if key == "" {
				key = stringValue(oauth["accessToken"])
			}
			a.config.ExpiresAt = milliseconds(oauth["expiresAt"])
			if a.config.ExpiresAt.IsZero() {
				a.config.ExpiresAt = tokenExpiry(key)
			}
			a.config.Secrets = append(a.config.Secrets, stringValue(oauth["refreshToken"]))
			a.account, err = readObject(filepath.Join(a.state, ".claude.json"), false)
			if err != nil {
				return a, err
			}
			if len(a.account) == 0 {
				a.account, err = readObject(filepath.Join(a.home, ".claude.json"), false)
				if err != nil {
					return a, err
				}
			}
		}
		value := key
		if header == "Authorization" {
			value = "Bearer " + key
		}
		a.config.Credentials = []credentialproxy.Credential{{Header: header, Value: value}}
	case "gemini":
		if isTrue(env["GOOGLE_GENAI_USE_VERTEXAI"]) || env["GOOGLE_APPLICATION_CREDENTIALS"] != "" {
			return a, errors.New("Vertex and application-default authentication are unsupported in session isolation")
		}
		if env["GEMINI_CLI_HOME"] != "" {
			a.home = env["GEMINI_CLI_HOME"]
		}
		a.state = filepath.Join(a.home, ".gemini")
		if a.settings, err = readObject(filepath.Join(a.state, "settings.json"), false); err != nil {
			return a, err
		}
		key = env.first("GEMINI_API_KEY", "GOOGLE_API_KEY")
		header := "x-goog-api-key"
		a.config.Upstream = env["GOOGLE_GEMINI_BASE_URL"]
		if a.config.Upstream == "" {
			a.config.Upstream = "https://generativelanguage.googleapis.com"
		}
		if key == "" {
			a.config.Native, header = true, "Authorization"
			login, err := readObject(filepath.Join(a.state, "oauth_creds.json"), false)
			if err != nil {
				return a, err
			}
			key = env["GOOGLE_CLOUD_ACCESS_TOKEN"]
			if key == "" {
				key = stringValue(login["access_token"])
			}
			a.config.ExpiresAt = milliseconds(login["expiry_date"])
			if a.config.ExpiresAt.IsZero() {
				a.config.ExpiresAt = tokenExpiry(key)
			}
			a.config.Secrets = append(a.config.Secrets, stringValue(login["refresh_token"]), stringValue(login["id_token"]))
			a.config.Project = env.first("GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT")
			a.config.Upstream = env["CODE_ASSIST_ENDPOINT"]
			if a.config.Upstream == "" {
				a.config.Upstream = "https://cloudcode-pa.googleapis.com"
			}
		}
		value := key
		if header == "Authorization" {
			value = "Bearer " + key
		}
		a.config.Credentials = []credentialproxy.Credential{{Header: header, Value: value}}
	default:
		return a, errors.New("unsupported session engine")
	}
	if !validPath(a.home) || !validPath(a.state) {
		return a, errors.New("invalid native CLI state path")
	}
	if key == "" {
		return a, errors.New("session isolation requires supported API authentication or a trusted native login")
	}
	if a.config.Native && a.config.ExpiresAt.IsZero() {
		return a, errors.New("native authentication expiry is unavailable; refresh the trusted login before using session isolation")
	}
	return a, nil
}

func sanitizedSettings(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for name, child := range value {
			if sensitiveName(name) || name == "env" || name == "apiKeyHelper" || name == "model_providers" || name == "model_provider" || name == "openai_base_url" || name == "include" {
				continue
			}
			out[name] = sanitizedSettings(child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			out[i] = sanitizedSettings(child)
		}
		return out
	default:
		return value
	}
}

// leaseConfiguration contains only temporary authentication. Upstream login
// files and arbitrary top-level state files are never mounted into the CLI.
func (a authentication) leaseConfiguration(lease *credentialproxy.Lease, env environment) (map[string][]byte, error) {
	files := map[string][]byte{}
	settings := object(sanitizedSettings(a.settings))
	put := func(name string, value any) error {
		data, err := json.Marshal(value)
		if err == nil {
			files[name] = data
		}
		return err
	}
	token, endpoint := lease.Token(), lease.Endpoint()
	switch a.config.Provider {
	case "codex":
		env["CODEX_HOME"] = a.state
		settings["sandbox_mode"] = "danger-full-access"
		if a.config.Native {
			settings["model_provider"], settings["openai_base_url"] = "openai", endpoint
			if err := put("auth.json", map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]string{"id_token": token, "access_token": token, "refresh_token": "wio-session-no-refresh", "account_id": a.config.AccountID}, "last_refresh": time.Now().UTC().Format(time.RFC3339)}); err != nil {
				return nil, err
			}
		} else {
			env["WIO_CODEX_API_KEY"] = token
			settings["model_provider"] = "wio_isolated"
			settings["model_providers"] = map[string]any{"wio_isolated": map[string]any{"name": "Wio session", "base_url": endpoint, "env_key": "WIO_CODEX_API_KEY", "wire_api": "responses"}}
			if err := put("auth.json", map[string]any{"OPENAI_API_KEY": token}); err != nil {
				return nil, err
			}
		}
		data, err := toml.Marshal(settings)
		if err != nil {
			return nil, errors.New("cannot prepare isolated Codex configuration")
		}
		files["config.toml"] = data
	case "claude":
		env["CLAUDE_CONFIG_DIR"], env["ANTHROPIC_BASE_URL"] = a.state, endpoint
		env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
		if a.config.Native {
			env["CLAUDE_CODE_OAUTH_TOKEN"] = token
			if err := put(".credentials.json", map[string]any{"claudeAiOauth": map[string]any{"accessToken": token, "refreshToken": "wio-session-no-refresh", "expiresAt": a.config.ExpiresAt.UnixMilli(), "scopes": []string{"user:inference", "user:profile"}}}); err != nil {
				return nil, err
			}
		} else if a.descriptor >= 0 {
			env[claudeKeyDescriptor] = strconv.Itoa(3 + a.descriptor)
		} else {
			env["ANTHROPIC_API_KEY"] = token
		}
		if err := put("settings.json", settings); err != nil {
			return nil, err
		}
		account := object(sanitizedSettings(a.account))
		account["hasCompletedOnboarding"] = true
		if err := put(".claude.json", account); err != nil {
			return nil, err
		}
	case "gemini":
		env["GEMINI_CLI_HOME"] = a.home
		authType := "gemini-api-key"
		if a.config.Native {
			authType = "oauth-personal"
			env["GOOGLE_GENAI_USE_GCA"], env["GOOGLE_CLOUD_ACCESS_TOKEN"] = "true", token
			env["GOOGLE_CLOUD_PROJECT"], env["CODE_ASSIST_ENDPOINT"], env["CODE_ASSIST_API_VERSION"] = a.config.Project, endpoint, "v1internal"
			if err := put("oauth_creds.json", map[string]any{"access_token": token, "refresh_token": "wio-session-no-refresh", "expiry_date": a.config.ExpiresAt.UnixMilli(), "token_type": "Bearer"}); err != nil {
				return nil, err
			}
		} else {
			env["GEMINI_API_KEY"], env["GOOGLE_GEMINI_BASE_URL"], env["GOOGLE_GENAI_API_VERSION"] = token, endpoint, "v1beta"
			env["GOOGLE_GENAI_USE_GCA"], env["GOOGLE_GENAI_USE_VERTEXAI"] = "false", "false"
		}
		env["GEMINI_CLI_AUTH_OVERRIDE"] = authType
		settings["security"] = map[string]any{"auth": map[string]string{"selectedType": authType, "enforcedType": authType}}
		if err := put("settings.json", settings); err != nil {
			return nil, err
		}
		env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] = filepath.Join(a.state, "settings.json")
		env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] = filepath.Join(a.state, "wio-missing-defaults.json")
	}
	return files, nil
}
