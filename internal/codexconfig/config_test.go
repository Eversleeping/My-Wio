package codexconfig

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestMergeAddsManagedSettingsAndPreservesExistingConfiguration(t *testing.T) {
	existing := []byte(`web_search = "live"

[features]
multi_agent = true

[sandbox_workspace_write]
writable_roots = ["/srv/shared"]
network_access = false

[model_providers.other]
name = "Other"
base_url = "https://other.example.com/v1"

[projects."/srv/project"]
trust_level = "trusted"
`)
	merged, err := Merge(existing, " https://api.example.com/v1/ ", " custom-model ")
	if err != nil {
		t.Fatal(err)
	}
	var configuration map[string]any
	if err := toml.Unmarshal(merged, &configuration); err != nil {
		t.Fatalf("merged configuration is invalid TOML: %v\n%s", err, merged)
	}
	for _, expected := range []string{
		`model = 'custom-model'`,
		`model_provider = 'wio_api'`,
		`sandbox_mode = 'workspace-write'`,
		`network_access = true`,
		`base_url = 'https://api.example.com/v1'`,
		`web_search = 'live'`,
		`writable_roots = ['/srv/shared']`,
		`trust_level = 'trusted'`,
		`name = 'Other'`,
	} {
		if !strings.Contains(strings.ReplaceAll(string(merged), `"`, "'"), expected) {
			t.Fatalf("configuration missing %q:\n%s", expected, merged)
		}
	}
}

func TestMergeRejectsInvalidExistingConfiguration(t *testing.T) {
	if _, err := Merge([]byte("[broken\n"), "https://api.example.com/v1", "custom-model"); err == nil {
		t.Fatal("invalid existing configuration was accepted")
	}
}
