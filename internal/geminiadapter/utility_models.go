package geminiadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"
)

// Native fallbacks keep non-Wio launches unchanged. Only this turn receives the
// gateway model. Keep loop detection enabled and honor explicit user aliases.
func loopModelEnvironment(environment []string, model string) ([]string, error) {
	values := map[string]string{}
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "WIO_GEMINI_LOOP_MODEL=") {
			result = append(result, entry)
		}
	}
	if values["GOOGLE_GEMINI_BASE_URL"] == "" || model == "" || model == "auto" {
		return result, nil
	}
	home, _ := os.UserHomeDir()
	if values["HOME"] != "" {
		home = values["HOME"]
	}
	if values["GEMINI_CLI_HOME"] != "" {
		home = values["GEMINI_CLI_HOME"]
	}
	if home == "" {
		return nil, errors.New("Gemini home directory unavailable")
	}
	if err := ensureLoopAliases(home); err != nil {
		return nil, err
	}
	return append(result, "WIO_GEMINI_LOOP_MODEL="+model), nil
}

func ensureLoopAliases(home string) error {
	thinkingSettingsMu.Lock()
	defer thinkingSettingsMu.Unlock()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe Gemini account settings directory")
	}
	path := filepath.Join(dir, "settings.json")
	settings, original, mode, err := readThinkingSettings(path)
	if err != nil {
		return err
	}
	configs, ok := settings["modelConfigs"].(map[string]any)
	if !ok && settings["modelConfigs"] != nil {
		return errors.New("invalid Gemini modelConfigs")
	}
	patches := []map[string]any{}
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
	builtins, _ := configs["aliases"].(map[string]any)
	added := false
	for _, item := range []struct{ name, base, fallback string }{
		{"loop-detection", "gemini-3-flash-base", "gemini-3-flash-preview"},
		{"loop-detection-double-check", "base", "gemini-3-pro-preview"},
	} {
		if _, exists := aliases[item.name]; exists {
			continue
		}
		if _, exists := builtins[item.name]; exists {
			continue
		}
		patches = append(patches, map[string]any{"op": "add", "path": "/modelConfigs/customAliases/" + item.name, "value": map[string]any{"extends": item.base, "modelConfig": map[string]string{"model": "${WIO_GEMINI_LOOP_MODEL:-" + item.fallback + "}"}}})
		added = true
	}
	if !added {
		return nil
	}
	existed := original != nil
	raw := original
	if raw == nil {
		raw = []byte("{}\n")
	}
	document, err := hujson.Parse(append([]byte(nil), raw...))
	if err != nil {
		return err
	}
	patch, _ := json.Marshal(patches)
	if err = document.Patch(patch); err != nil {
		return err
	}
	document.Format()
	current, readErr := os.ReadFile(path)
	if (existed && (readErr != nil || !bytes.Equal(current, original))) || (!existed && !os.IsNotExist(readErr)) {
		return errors.New("Gemini account settings changed; retry after saving configuration")
	}
	return writeThinkingSettings(path, document.Pack(), mode)
}
