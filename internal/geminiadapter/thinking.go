package geminiadapter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tailscale/hujson"
	"github.com/wio-platform/wio/internal/geminiapi"
)

// Use the CLI's official system override layer only for this process. Preserve
// existing admin settings and defaults; never edit user/workspace settings.
func thinkingEnvironment(environment []string, model, level, workspace string) ([]string, func(), error) {
	return prepareThinkingEnvironment(environment, model, level, workspace, createThinkingSettingsFile)
}

func prepareThinkingEnvironment(environment []string, model, level, workspace string, createFile func(string) (*os.File, error)) ([]string, func(), error) {
	noop := func() {}
	if !geminiapi.ValidThinkingLevel(model, level) {
		return nil, noop, errors.New("invalid Gemini thinking level for this model")
	}
	if level == "" {
		return environment, noop, nil
	}
	systemPath := "/etc/gemini-cli/settings.json"
	if runtime.GOOS == "windows" {
		systemPath = `C:\ProgramData\gemini-cli\settings.json`
	}
	if runtime.GOOS == "darwin" {
		systemPath = "/Library/Application Support/GeminiCli/settings.json"
	}
	defaultsPath := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "GEMINI_CLI_SYSTEM_SETTINGS_PATH="); ok && value != "" {
			systemPath = value
		}
		if value, ok := strings.CutPrefix(entry, "GEMINI_CLI_SYSTEM_DEFAULTS_PATH="); ok {
			defaultsPath = value
		}
	}
	if defaultsPath == "" {
		defaultsPath = filepath.Join(filepath.Dir(systemPath), "system-defaults.json")
	}
	settings := map[string]any{}
	if raw, err := os.ReadFile(systemPath); err == nil {
		raw, err = hujson.Standardize(raw)
		if err != nil || json.Unmarshal(raw, &settings) != nil || settings == nil {
			return nil, noop, errors.New("could not parse Gemini system settings; original configuration was kept")
		}
	} else if !os.IsNotExist(err) {
		return nil, noop, errors.New("could not read Gemini system settings")
	}
	configs, ok := settings["modelConfigs"].(map[string]any)
	if !ok && settings["modelConfigs"] != nil {
		return nil, noop, errors.New("invalid Gemini modelConfigs")
	}
	if configs == nil {
		configs = map[string]any{}
		settings["modelConfigs"] = configs
	}
	// Arrays in CLI settings are replaced by higher layers. Retain the
	// effective inherited array before adding the process-local override.
	if _, exists := configs["customOverrides"]; !exists {
		inherited, err := inheritedThinkingOverrides(environment, workspace, defaultsPath)
		if err != nil {
			return nil, noop, err
		}
		configs["customOverrides"] = inherited
	}
	overrides, ok := configs["customOverrides"].([]any)
	if !ok && configs["customOverrides"] != nil {
		return nil, noop, errors.New("invalid Gemini model overrides")
	}
	configs["customOverrides"] = append(overrides, map[string]any{
		"match":       map[string]any{"model": model},
		"modelConfig": map[string]any{"generateContentConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": strings.ToUpper(level)}}},
	})
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, noop, err
	}
	f, err := createFile(filepath.Dir(systemPath))
	if err != nil {
		return nil, noop, errors.New("could not prepare Gemini thinking settings: the Agent needs an administrator-owned Gemini system configuration directory")
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err = f.Write(raw); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		cleanup()
		return nil, noop, errors.New("could not save Gemini thinking settings")
	}
	result := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=") && !strings.HasPrefix(entry, "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=") {
			result = append(result, entry)
		}
	}
	result = append(result, "GEMINI_CLI_SYSTEM_SETTINGS_PATH="+f.Name(), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH="+defaultsPath)
	return result, cleanup, nil
}

// The CLI rejects system settings in /tmp or under non-administrator-owned
// ancestors. Keep per-turn files alongside the original system settings.
func createThinkingSettingsFile(directory string) (*os.File, error) {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return nil, err
	}
	if err := validateThinkingDirectory(directory); err != nil {
		return nil, err
	}
	return os.CreateTemp(directory, "wio-gemini-thinking-*.json")
}

func inheritedThinkingOverrides(environment []string, workspace, defaultsPath string) ([]any, error) {
	home, _ := os.UserHomeDir()
	trusted := false
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = value
		}
		if entry == "GEMINI_CLI_TRUST_WORKSPACE=true" {
			trusted = true
		}
	}
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "GEMINI_CLI_HOME="); ok && value != "" {
			home = value
		}
	}
	paths := []string{}
	// The CLI ignores insecure system defaults. Do not import them into a
	// secure override file and accidentally grant them system precedence.
	if validateThinkingDirectory(defaultsPath) == nil {
		if info, err := os.Stat(defaultsPath); err == nil && info.Mode().Perm()&0022 == 0 {
			paths = append(paths, defaultsPath)
		}
	}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".gemini", "settings.json"))
	}
	if trusted && workspace != "" && filepath.Clean(workspace) != filepath.Clean(home) {
		paths = append(paths, filepath.Join(workspace, ".gemini", "settings.json"))
	}
	var overrides []any
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("could not read inherited Gemini model settings")
		}
		raw, err = hujson.Standardize(raw)
		var settings map[string]any
		if err != nil || json.Unmarshal(raw, &settings) != nil {
			return nil, errors.New("could not parse inherited Gemini model settings")
		}
		if configs, ok := settings["modelConfigs"].(map[string]any); ok {
			if value, exists := configs["customOverrides"]; exists {
				var valid bool
				overrides, valid = value.([]any)
				if !valid {
					return nil, errors.New("invalid inherited Gemini model overrides")
				}
			}
		}
	}
	return overrides, nil
}
