package geminiadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tailscale/hujson"
	"github.com/wio-platform/wio/internal/geminiapi"
)

const thinkingAlias = "wio-internal-thinking"

var thinkingSettingsMu sync.Mutex

// Install one inert official CLI alias in the Agent account. Each process
// supplies its model and level through the CLI's documented environment
// interpolation. This works without administrator privileges, preserves native
// history paths, and never places generated settings in a Git workspace.
func thinkingEnvironment(environment []string, model, level string) ([]string, string, error) {
	if !geminiapi.ValidThinkingLevel(model, level) || model == thinkingAlias {
		return nil, "", errors.New("invalid Gemini thinking level for this model")
	}
	if level == "" {
		return environment, model, nil
	}
	home, _ := os.UserHomeDir()
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = value
		}
	}
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "GEMINI_CLI_HOME="); ok && value != "" {
			home = value
		}
	}
	if home == "" {
		return nil, "", errors.New("Gemini home directory unavailable")
	}
	if err := ensureThinkingAlias(home); err != nil {
		return nil, "", err
	}
	result := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "WIO_GEMINI_THINKING_MODEL=") && !strings.HasPrefix(entry, "WIO_GEMINI_THINKING_LEVEL=") {
			result = append(result, entry)
		}
	}
	result = append(result, "WIO_GEMINI_THINKING_MODEL="+model, "WIO_GEMINI_THINKING_LEVEL="+strings.ToUpper(level))
	return result, thinkingAlias, nil
}

func ensureThinkingAlias(home string) error {
	thinkingSettingsMu.Lock()
	defer thinkingSettingsMu.Unlock()
	directory := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("could not prepare Gemini account settings")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Gemini account settings directory must be a regular directory")
	}
	path := filepath.Join(directory, "settings.json")
	settings, original, mode, err := readThinkingSettings(path)
	if err != nil {
		return err
	}
	desired := map[string]any{
		"extends": "chat-base",
		"modelConfig": map[string]any{
			"model":                 "${WIO_GEMINI_THINKING_MODEL:-gemini-3-flash-preview}",
			"generateContentConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "${WIO_GEMINI_THINKING_LEVEL:-HIGH}"}},
		},
	}
	patches := []map[string]any{}
	configs, ok := settings["modelConfigs"].(map[string]any)
	if !ok && settings["modelConfigs"] != nil {
		return errors.New("invalid Gemini modelConfigs")
	}
	if configs == nil {
		configs = map[string]any{}
		patches = append(patches, map[string]any{"op": "add", "path": "/modelConfigs", "value": configs})
	}
	aliases, ok := configs["customAliases"].(map[string]any)
	if !ok && configs["customAliases"] != nil {
		return errors.New("invalid Gemini custom aliases")
	}
	if aliases == nil {
		aliases = map[string]any{}
		patches = append(patches, map[string]any{"op": "add", "path": "/modelConfigs/customAliases", "value": aliases})
	}
	if existing, ok := aliases[thinkingAlias]; ok {
		a, _ := json.Marshal(existing)
		b, _ := json.Marshal(desired)
		if !bytes.Equal(a, b) {
			return errors.New("Gemini's Wio thinking alias conflicts with an existing custom alias")
		}
		return nil
	}
	patches = append(patches, map[string]any{"op": "add", "path": "/modelConfigs/customAliases/" + thinkingAlias, "value": desired})
	existed := original != nil
	if original == nil {
		original = []byte("{}\n")
	}
	document, err := hujson.Parse(append([]byte(nil), original...))
	if err != nil {
		return errors.New("could not parse Gemini account settings")
	}
	patch, _ := json.Marshal(patches)
	if err = document.Patch(patch); err != nil {
		return errors.New("could not add Gemini thinking alias")
	}
	document.Format()
	// Recheck before replacing a settings file that a user may have edited.
	current, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return errors.New("could not recheck Gemini account settings")
	}
	if (existed && (readErr != nil || !bytes.Equal(current, original))) || (!existed && !os.IsNotExist(readErr)) {
		return errors.New("Gemini account settings changed; retry after saving the configuration")
	}
	if err = writeThinkingSettings(path, document.Pack(), mode); err != nil {
		return errors.New("could not save Gemini thinking alias")
	}
	return nil
}

func readThinkingSettings(path string) (map[string]any, []byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil, 0600, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, nil, 0, errors.New("Gemini settings must be a regular JSON file no larger than 2 MiB")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, 0, errors.New("could not read Gemini settings")
	}
	raw, err := hujson.Standardize(append([]byte(nil), original...))
	var settings map[string]any
	if err != nil || json.Unmarshal(raw, &settings) != nil || settings == nil {
		return nil, nil, 0, errors.New("could not parse Gemini settings; original configuration was kept")
	}
	return settings, original, info.Mode().Perm(), nil
}

func writeThinkingSettings(path string, raw []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".wio-thinking-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Chmod(file.Name(), mode); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
