package codexconfig

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const providerName = "wio_api"

// SandboxMode allows Codex to update Git metadata such as .git. The Agent's
// systemd unit still constrains ordinary installations to its managed roots.
const SandboxMode = "danger-full-access"

// Merge updates the settings managed by Wio while retaining unrelated Codex
// configuration, such as trusted projects and user-selected feature flags.
func Merge(existing []byte, apiURL, model string) ([]byte, error) {
	configuration := map[string]any{}
	if strings.TrimSpace(string(existing)) != "" {
		if err := toml.Unmarshal(existing, &configuration); err != nil {
			return nil, fmt.Errorf("parse existing Codex configuration: %w", err)
		}
	}

	configuration["model"] = strings.TrimSpace(model)
	configuration["model_provider"] = providerName
	configuration["model_supports_reasoning_summaries"] = true
	configuration["sandbox_mode"] = SandboxMode

	sandbox := table(configuration, "sandbox_workspace_write")
	sandbox["network_access"] = true
	features := table(configuration, "features")
	features["goals"] = true
	features["multi_agent"] = true
	agents := table(configuration, "agents")
	agents["enabled"] = true

	providers := table(configuration, "model_providers")
	provider := table(providers, providerName)
	provider["name"] = "Wio API"
	provider["base_url"] = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	provider["env_key"] = "WIO_CODEX_API_KEY"
	provider["wire_api"] = "responses"

	result, err := toml.Marshal(configuration)
	if err != nil {
		return nil, fmt.Errorf("encode Codex configuration: %w", err)
	}
	return result, nil
}

func table(parent map[string]any, name string) map[string]any {
	if existing, ok := parent[name].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	parent[name] = created
	return created
}
