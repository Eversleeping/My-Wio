package childprocess

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// npmEntry resolves only the npm commands Wio manages. The package manifest is
// authoritative: upstream can move a JS entry point or replace it with an exe.
// We never evaluate the .cmd shim or interpolate arguments into a shell.
func npmEntry(wrapper string) (entry string, managed bool, err error) {
	ext := strings.ToLower(filepath.Ext(wrapper))
	if ext != ".cmd" && ext != ".bat" {
		return "", false, nil
	}
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(wrapper), filepath.Ext(wrapper)))
	packages := map[string]string{"codex": "@openai/codex", "gemini": "@google/gemini-cli", "claude": "@anthropic-ai/claude-code", "npm": "npm", "npx": "npm"}
	pkg, ok := packages[name]
	if !ok {
		return "", false, nil
	}
	root := filepath.Join(filepath.Dir(wrapper), "node_modules")
	if strings.EqualFold(filepath.Base(filepath.Dir(wrapper)), ".bin") {
		root = filepath.Dir(filepath.Dir(wrapper))
	}
	packageRoot := filepath.Join(root, filepath.FromSlash(pkg))
	raw, err := os.ReadFile(filepath.Join(packageRoot, "package.json"))
	if err != nil {
		return "", true, fmt.Errorf("read %s npm entry point: %w", name, err)
	}
	var manifest struct {
		Bin json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", true, fmt.Errorf("read %s npm manifest: %w", name, err)
	}
	var relative string
	if json.Unmarshal(manifest.Bin, &relative) != nil {
		var bins map[string]string
		if err := json.Unmarshal(manifest.Bin, &bins); err != nil {
			return "", true, fmt.Errorf("invalid %s npm bin declaration", name)
		}
		relative = bins[name]
	}
	relative = filepath.FromSlash(relative)
	if relative == "" || !filepath.IsLocal(relative) {
		return "", true, fmt.Errorf("invalid %s npm entry point", name)
	}
	entry = filepath.Join(packageRoot, relative)
	info, err := os.Stat(entry)
	if err != nil {
		return "", true, fmt.Errorf("find %s npm entry point: %w", name, err)
	}
	if info.IsDir() {
		return "", true, fmt.Errorf("invalid %s npm entry point directory", name)
	}
	return entry, true, nil
}
