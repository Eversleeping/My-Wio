package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/sessionisolation"
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
		t.Fatal("the loaded Agent configuration path was not retained for isolation")
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

func TestSessionIsolationConfigDefaultsAndSudoConflict(t *testing.T) {
	config := Config{ControlURL: "http://127.0.0.1:12345", ServerID: "test-server", AgentToken: "test-token"}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.SessionIsolation != nil {
		t.Fatal("existing configurations enabled isolation")
	}
	config.SessionIsolation = &sessionisolation.Config{Enabled: true, UID: 1000, GID: 1000, RuntimePaths: []string{"/opt/public-runtime"}}
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfig(path)
	if err != nil || loaded.SessionIsolation == nil || !loaded.SessionIsolation.Enabled || loaded.SessionIsolation.UID != 1000 || !slices.Equal(loaded.SessionIsolation.RuntimePaths, config.SessionIsolation.RuntimePaths) {
		t.Fatal("isolation configuration was not preserved")
	}
	config.AllowSudo = true
	if err := SaveConfig(path, config); err == nil {
		t.Fatal("isolation and passwordless sudo were accepted together")
	}
	loaded, err = LoadConfig(path)
	if err != nil || loaded.AllowSudo || !loaded.SessionIsolation.Enabled {
		t.Fatal("rejected configuration overwrote the existing settings")
	}
	config.AllowSudo = false
	config.SessionIsolation.RuntimePaths = []string{"/"}
	if err := SaveConfig(path, config); err == nil {
		t.Fatal("an unrestricted runtime mount was accepted")
	}
}
