package geminiadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestThinkingSettingsPreserveAdminConfigAndAreProcessScoped(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "settings.json")
	original := []byte(`{ // Admin JSONC must remain intact.
 "security":{"disableYoloMode":true},
 "modelConfigs":{"customOverrides":[{"match":{"model":"other"},"modelConfig":{"generateContentConfig":{"temperature":0.2}}}]},
}`)
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	first, cleanup, err := prepareThinkingEnvironment([]string{"GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + source}, "gemini-3-flash-preview", "low", "", func(directory string) (*os.File, error) { return os.CreateTemp(directory, "thinking-*.json") })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var path string
	for _, entry := range first {
		if v, ok := strings.CutPrefix(entry, "GEMINI_CLI_SYSTEM_SETTINGS_PATH="); ok {
			path = v
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Security struct {
			DisableYoloMode bool `json:"disableYoloMode"`
		} `json:"security"`
		ModelConfigs struct {
			CustomOverrides []struct {
				ModelConfig struct {
					GenerateContentConfig struct {
						ThinkingConfig struct {
							ThinkingLevel string `json:"thinkingLevel"`
						} `json:"thinkingConfig"`
					} `json:"generateContentConfig"`
				} `json:"modelConfig"`
			} `json:"customOverrides"`
		} `json:"modelConfigs"`
	}
	if err = json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Security.DisableYoloMode || len(settings.ModelConfigs.CustomOverrides) != 2 || settings.ModelConfigs.CustomOverrides[1].ModelConfig.GenerateContentConfig.ThinkingConfig.ThinkingLevel != "LOW" {
		t.Fatal("thinking override replaced admin settings or did not reach CLI config")
	}
	if !strings.Contains(strings.Join(first, "\n"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH="+filepath.Join(root, "system-defaults.json")) {
		t.Fatal("system defaults moved with the temporary override")
	}
	after, _ := os.ReadFile(source)
	if string(after) != string(original) {
		t.Fatal("persistent settings changed")
	}
	cleanup()
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temporary thinking settings were retained")
	}
	plain, none, err := thinkingEnvironment([]string{"sentinel=value"}, "auto", "", "")
	none()
	if err != nil || len(plain) != 1 {
		t.Fatal("default mode unexpectedly changed CLI settings")
	}
	if _, _, err = thinkingEnvironment(nil, "gemini-3.8-flash-high", "low", ""); err == nil {
		t.Fatal("conflicting upstream preset accepted")
	}
}

func TestRunPassesThinkingConfigurationToCLIAndCleansIt(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		t.Skip("official CLI system overrides require a root-owned file")
	}
	t.Setenv("WIO_GEMINI_TEST_CHILD", "success")
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_GEMINI_TEST_RECORD", record)
	binary, _ := os.Executable()
	a := New(binary, func(protocol.StreamEvent) error { return nil })
	if err := a.Run(context.Background(), "thinking", protocol.GeminiTurnCommand{Workspace: t.TempDir(), Model: "gemini-3-flash-preview", ApprovalMode: "default", ThinkingLevel: "medium", Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	var captured struct {
		ThinkingPath     string
		ThinkingSettings json.RawMessage
	}
	raw, _ := os.ReadFile(record)
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(captured.ThinkingSettings), `"thinkingLevel":"MEDIUM"`) {
		t.Fatalf("CLI did not read requested thinking configuration: %s", captured.ThinkingSettings)
	}
	if _, err := os.Stat(captured.ThinkingPath); !os.IsNotExist(err) {
		t.Fatal("thinking file survived completion")
	}
}

func TestThinkingPreservesInheritedOverridesAndWorkspaceTrust(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	for directory, temperature := range map[string]string{home: "0.2", workspace: "0.4"} {
		if err := os.Mkdir(filepath.Join(directory, ".gemini"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, ".gemini", "settings.json"), []byte(`{"modelConfigs":{"customOverrides":[{"match":{"model":"other"},"modelConfig":{"generateContentConfig":{"temperature":`+temperature+`}}}]}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{"GEMINI_CLI_HOME=" + home}
	for _, trusted := range []bool{false, true} {
		env := append([]string{}, environment...)
		if trusted {
			env = append(env, "GEMINI_CLI_TRUST_WORKSPACE=true")
		}
		overrides, err := inheritedThinkingOverrides(env, workspace, filepath.Join(t.TempDir(), "missing.json"))
		if err != nil || len(overrides) != 1 {
			t.Fatalf("inherited overrides: %v %v", overrides, err)
		}
		raw, _ := json.Marshal(overrides)
		expected := `"temperature":0.2`
		if trusted {
			expected = `"temperature":0.4`
		}
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("wrong trust/precedence: %s", raw)
		}
	}
	// An explicit empty workspace array also replaces user overrides.
	if err := os.WriteFile(filepath.Join(workspace, ".gemini", "settings.json"), []byte(`{"modelConfigs":{"customOverrides":[]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	overrides, err := inheritedThinkingOverrides(append(environment, "GEMINI_CLI_TRUST_WORKSPACE=true"), workspace, filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || len(overrides) != 0 {
		t.Fatal("explicit empty array did not replace inherited settings")
	}
}
