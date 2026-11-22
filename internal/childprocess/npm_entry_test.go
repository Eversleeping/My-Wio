package childprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNPMEntryUsesPackageManifest(t *testing.T) {
	for _, tc := range []struct {
		name, pkg, entry string
		global, scalar   bool
	}{
		{"gemini", "@google/gemini-cli", "dist/index.js", false, false},
		{"gemini", "@google/gemini-cli", "bundle/gemini.js", false, false},
		{"claude", "@anthropic-ai/claude-code", "cli.js", false, true},
		{"claude", "@anthropic-ai/claude-code", "bin/claude.exe", false, false},
		{"codex", "@openai/codex", "bin/codex.js", false, false},
		{"npm", "npm", "bin/npm-cli.js", true, false},
		{"npx", "npm", "bin/npx-cli.js", true, false},
	} {
		t.Run(tc.name+"/"+tc.entry, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "npm prefix")
			wrapper := filepath.Join(prefix, "node_modules", ".bin", tc.name+".cmd")
			if tc.global {
				wrapper = filepath.Join(prefix, tc.name+".cmd")
			}
			root := filepath.Join(prefix, "node_modules", filepath.FromSlash(tc.pkg))
			entry := filepath.Join(root, filepath.FromSlash(tc.entry))
			if err := os.MkdirAll(filepath.Dir(entry), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(entry, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			var bin any = map[string]string{tc.name: tc.entry, "another-command": "wrong.js"}
			if tc.scalar {
				bin = tc.entry
			}
			raw, _ := json.Marshal(map[string]any{"bin": bin})
			if err := os.WriteFile(filepath.Join(root, "package.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			got, managed, err := npmEntry(wrapper)
			if err != nil || !managed || got != entry {
				t.Fatalf("entry=%q managed=%v error=%v", got, managed, err)
			}
		})
	}
}

func TestNPMEntryRejectsInvalidManifestOrEntry(t *testing.T) {
	for _, raw := range []string{`{`, `{}`, `{"bin":{"other":"index.js"}}`, `{"bin":42}`, `{"bin":{"gemini":"../escape.js"}}`, `{"bin":{"gemini":"/absolute.js"}}`, `{"bin":{"gemini":"missing.js"}}`, `{"bin":{"gemini":"directory"}}`} {
		t.Run(raw, func(t *testing.T) {
			prefix := t.TempDir()
			root := filepath.Join(prefix, "node_modules", "@google", "gemini-cli")
			if err := os.MkdirAll(filepath.Join(root, "directory"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, managed, err := npmEntry(filepath.Join(prefix, "node_modules", ".bin", "gemini.cmd")); err == nil || !managed {
				t.Fatalf("invalid entry accepted: managed=%v error=%v", managed, err)
			}
		})
	}
	for _, name := range []string{"other.cmd", "gemini.exe", "gemini.js"} {
		if _, managed, err := npmEntry(name); managed || err != nil {
			t.Fatalf("unmanaged command intercepted: %s", name)
		}
	}
}
