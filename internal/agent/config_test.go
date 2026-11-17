package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestConfigExtraEnvironmentStoresNamesOnly(t *testing.T) {
	t.Setenv("CUSTOM_TOOLCHAIN_TOKEN", "synthetic-service-value")
	config := Config{ControlURL: "http://127.0.0.1:12345", ServerID: "test-server", AgentToken: "test-token", ExtraEnvironment: []string{"GOPATH", "CUSTOM_TOOLCHAIN_TOKEN"}}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || !slices.Equal(loaded.ExtraEnvironment, config.ExtraEnvironment) {
		t.Fatal("extra environment names did not survive the configuration round trip", err)
	}
	if loaded.configPath != path {
		t.Fatal("the loaded Agent configuration path was not retained for runtime settings")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-service-value") {
		t.Fatal("configuration persisted the service environment value")
	}
	for _, name := range []string{"WIO_MASTER_KEY", "wio_database_url", "*", "KEY=synthetic-service-value"} {
		config.ExtraEnvironment = []string{name}
		if err := SaveConfig(path, config); err == nil || strings.Contains(err.Error(), "synthetic-service-value") {
			t.Fatal("unsafe extra environment entry was accepted or exposed in an error")
		}
	}
}

func TestLegacyIsolationConfigDoesNotBlockSudoOrPersistAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"control_url":"http://127.0.0.1:12345","server_id":"node","agent_token":"synthetic-token","allow_sudo":true,"session_isolation":{"enabled":true,"uid":-1,"runtime_paths":["/"]}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil || !config.AllowSudo {
		t.Fatalf("retired config affected loading: %v", err)
	}
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(saved), "session_isolation") {
		t.Fatal("retired config persisted", err)
	}
}
