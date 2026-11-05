package claudeadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func runSkillsTestChild(mode string) {
	line, _ := bufio.NewReader(os.Stdin).ReadBytes('\n')
	var request struct {
		Type    string `json:"type"`
		ID      string `json:"request_id"`
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	_ = json.Unmarshal(line, &request)
	raw, _ := json.Marshal(map[string]any{"args": os.Args[1:], "stdin": string(line)})
	_ = os.WriteFile(os.Getenv("WIO_CLAUDE_TEST_RECORD"), raw, 0600)
	if request.Type != "control_request" || request.Request.Subtype != "initialize" || request.ID == "" {
		os.Exit(2)
	}
	if mode == "skills-wait" {
		time.Sleep(time.Hour)
		return
	}
	if mode == "skills-invalid" {
		fmt.Println("private-provider-diagnostic")
		return
	}
	if mode == "skills-failed" {
		fmt.Printf("{\"type\":\"control_response\",\"response\":{\"request_id\":%q,\"subtype\":\"error\",\"error\":\"private-provider-diagnostic\"}}\n", request.ID)
		return
	}
	fmt.Println(`{"type":"control_response","response":{"request_id":"wrong-id","subtype":"success","response":{"commands":[{"name":"wrong"}]}}}`)
	commands := []map[string]any{
		{"name": "compact", "builtin": true},
		{"name": "review", "description": "Review changes", "argumentHint": "[scope]"},
		{"name": "plugin:code.review", "description": strings.Repeat("技", 2000)},
		{"name": "team/review"}, {"name": "review"}, {"name": "bad name"}, {"name": "--bad"},
	}
	response := map[string]any{"type": "control_response", "response": map[string]any{"request_id": request.ID, "subtype": "success", "response": map[string]any{"commands": commands}}}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	// The caller must terminate this otherwise persistent SDK process.
	time.Sleep(time.Hour)
}

func TestListSkillsInitializesWithoutModelTurnAndFiltersCommands(t *testing.T) {
	t.Setenv("WIO_CLAUDE_TEST_CHILD", "skills-success")
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_CLAUDE_TEST_RECORD", record)
	binary, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := ListSkills(ctx, binary, t.TempDir())
	if err != nil || len(result.Skills) != 3 {
		t.Fatalf("skills=%+v err=%v", result.Skills, err)
	}
	if result.Skills[0].Name != "review" || result.Skills[0].ArgumentHint != "[scope]" || result.Skills[1].Name != "plugin:code.review" || !utf8.ValidString(result.Skills[1].Description) || len(result.Skills[1].Description) > 4096 || result.Skills[2].Name != "team/review" {
		t.Fatalf("bad metadata: %+v", result.Skills)
	}
	raw, _ := os.ReadFile(record)
	var captured struct {
		Args  []string
		Stdin string
	}
	if json.Unmarshal(raw, &captured) != nil {
		t.Fatal("missing discovery invocation")
	}
	if strings.Contains(captured.Stdin, `"user"`) || !strings.Contains(strings.Join(captured.Args, " "), "--no-session-persistence") {
		t.Fatalf("discovery started a turn: %s", raw)
	}
}

func TestListSkillsHidesDiagnosticsAndHonorsCancellation(t *testing.T) {
	binary, _ := os.Executable()
	for _, mode := range []string{"skills-invalid", "skills-failed", "skills-wait"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WIO_CLAUDE_TEST_CHILD", mode)
			t.Setenv("WIO_CLAUDE_TEST_RECORD", filepath.Join(t.TempDir(), "record"))
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_, err := ListSkills(ctx, binary, t.TempDir())
			if err == nil || strings.Contains(err.Error(), "private-provider") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
