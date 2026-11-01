package geminiadapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestParseGeminiSkills(t *testing.T) {
	output := "A harmless startup warning\r\nDiscovered Agent Skills:\r\n\r\n" +
		"local-skill [Enabled]\r\n  Description: First skill\r\n  Location: /private/location\r\n\r\n" +
		"built_in [Disabled] [Built-in]\r\n  Description: " + strings.Repeat("文", 520) + "\r\n"
	result, err := parseGeminiSkills(output)
	if err != nil || len(result.Skills) != 2 {
		t.Fatalf("skills: %+v %v", result, err)
	}
	if first := result.Skills[0]; first.Name != "local-skill" || !first.Enabled || first.Builtin || first.Description != "First skill" {
		t.Fatalf("local skill: %+v", first)
	}
	if second := result.Skills[1]; second.Name != "built_in" || second.Enabled || !second.Builtin || len([]rune(second.Description)) != 512 {
		t.Fatalf("built-in skill: %+v", second)
	}
	result, err = parseGeminiSkills("startup warning\nNo skills discovered.\n")
	if err != nil || result.Skills == nil || len(result.Skills) != 0 {
		t.Fatalf("empty skills: %+v %v", result, err)
	}
	if _, err := parseGeminiSkills("unexpected response\n"); err == nil {
		t.Fatal("accepted unrecognized CLI output")
	}
	if _, err := parseGeminiSkills(strings.Repeat("skill [Enabled]\n", 501)); err == nil {
		t.Fatal("accepted too many skills")
	}
	var buffer boundedSkillBuffer
	if _, err := buffer.Write([]byte(strings.Repeat("x", maxGeminiSkillsOutput+1))); err == nil {
		t.Fatal("accepted oversized CLI output")
	}
}

func TestNativeGeminiSkillManagementLifecycle(t *testing.T) {
	cli := os.Getenv("WIO_GEMINI_STEERING_CLI")
	if cli == "" {
		t.Skip("set WIO_GEMINI_STEERING_CLI for official CLI integration")
	}
	for _, scope := range []string{"workspace", "user"} {
		t.Run(scope, func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			source := filepath.Join(workspace, "source", "wio-lifecycle")
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: wio-lifecycle\ndescription: Test skill lifecycle\n---\nLocal test instructions.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			a := New(cli, nil)
			a.environment = []string{"GEMINI_CLI_HOME=" + home, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")}
			for _, action := range []string{"install", "disable", "enable", "uninstall"} {
				cmd := protocol.GeminiSkillManageCommand{Workspace: workspace, Action: action, Scope: scope, Name: "wio-lifecycle"}
				if action == "install" {
					cmd.Name = ""
					cmd.Source = source
				}
				result, err := a.ManageSkill(context.Background(), cmd)
				if err != nil {
					t.Fatalf("%s: %v", action, err)
				}
				found := false
				for _, skill := range result.Skills {
					if skill.Name == "wio-lifecycle" {
						found = true
						if skill.Scope != scope || skill.Enabled != (action != "disable") {
							t.Fatalf("%s: %+v", action, skill)
						}
					}
				}
				if found != (action != "uninstall") {
					t.Fatalf("%s: skill found=%v", action, found)
				}
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.MkdirAll(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := a.ManageSkill(context.Background(), protocol.GeminiSkillManageCommand{Workspace: workspace, Action: "install", Source: outside, Scope: scope}); err == nil {
				t.Fatal("accepted source outside workspace")
			}
		})
	}
}

// Optional integration with an actual official CLI; no external credentials are used.
func TestNativeGeminiSkillsDiscovery(t *testing.T) {
	cli := os.Getenv("WIO_GEMINI_STEERING_CLI")
	if cli == "" {
		t.Skip("set WIO_GEMINI_STEERING_CLI to run the official CLI integration")
	}
	home, workspace := t.TempDir(), t.TempDir()
	dir := filepath.Join(workspace, ".gemini", "skills", "wio-test-skill")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: wio-test-skill\ndescription: A workspace test skill\n---\n\nTest instructions.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := New(cli, nil)
	a.environment = []string{"GEMINI_CLI_HOME=" + home, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")}
	result, err := a.ListSkills(context.Background(), "skill-test", workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range result.Skills {
		if skill.Name == "wio-test-skill" && skill.Enabled && skill.Description == "A workspace test skill" {
			return
		}
	}
	t.Fatalf("workspace skill not discovered: %+v", result.Skills)
}
