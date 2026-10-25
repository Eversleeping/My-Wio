package geminiadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/wio-platform/wio/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestThinkingAliasPreservesAccountSettingsAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".gemini")
	os.Mkdir(directory, 0700)
	path := filepath.Join(directory, "settings.json")
	original := []byte(`{ // Keep the administrator's comments and unrelated settings.
 "security":{"disableYoloMode":true},
 "modelConfigs":{"customAliases":{"my-model":{"extends":"chat-base","modelConfig":{"model":"other"}}},"customOverrides":[{"match":{"model":"other"},"modelConfig":{"generateContentConfig":{"temperature":0.2}}}]},
}`)
	os.WriteFile(path, original, 0600)
	env := []string{"GEMINI_CLI_HOME=" + home, "sentinel=value"}
	result, alias, err := thinkingEnvironment(env, "gemini-3-flash-preview", "low")
	if err != nil {
		t.Fatal(err)
	}
	if alias != thinkingAlias || !strings.Contains(strings.Join(result, "\n"), "WIO_GEMINI_THINKING_LEVEL=LOW") {
		t.Fatal("model/level not configured")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "Keep the administrator's comments") || !strings.Contains(string(raw), `"my-model"`) {
		t.Fatal("existing JSONC was replaced")
	}
	settings, _, _, err := readThinkingSettings(path)
	if err != nil || settings["security"].(map[string]any)["disableYoloMode"] != true {
		t.Fatal("original account configuration changed")
	}
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, _, err := thinkingEnvironment(env, "gemini-3-flash-preview", "medium"); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) {
		t.Fatal("idempotent alias installation rewrote settings")
	}
	if _, model, err := thinkingEnvironment(env, "auto", ""); err != nil || model != "auto" {
		t.Fatal("default mode changed")
	}
	if _, _, err := thinkingEnvironment(env, "gemini-3.8-flash-high", "low"); err == nil {
		t.Fatal("conflicting preset accepted")
	}
}
func TestRunUsesNativeThinkingAliasWithoutChangingWorkspace(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "success")
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_GEMINI_TEST_RECORD", record)
	binary, _ := os.Executable()
	a := New(binary, func(protocol.StreamEvent) error { return nil })
	root := t.TempDir()
	if err := a.RunInTrustedWorkspace(context.Background(), "thinking", protocol.GeminiTurnCommand{Workspace: root, Model: "gemini-3-flash-preview", ApprovalMode: "default", ThinkingLevel: "medium", Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Args             []string
		ThinkingModel    string
		ThinkingLevel    string
		ThinkingSettings json.RawMessage
	}
	raw, _ := os.ReadFile(record)
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	if len(captured.Args) < 4 || captured.Args[3] != thinkingAlias || captured.ThinkingModel != "gemini-3-flash-preview" || captured.ThinkingLevel != "MEDIUM" || !strings.Contains(string(captured.ThinkingSettings), thinkingAlias) {
		t.Fatal("thinking alias did not reach CLI")
	}
	if _, err := os.Stat(filepath.Join(root, ".gemini")); !os.IsNotExist(err) {
		t.Fatal("generated settings were written to the project")
	}
}
