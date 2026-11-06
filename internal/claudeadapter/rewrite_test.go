package claudeadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestRewriteBoundaryAndLegacyMatching(t *testing.T) {
	first, answer, target, later := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	line := func(id, parent, kind, text, stop string) string {
		raw, _ := json.Marshal(map[string]any{"uuid": id, "parentUuid": parent, "type": kind, "timestamp": "2026-11-06T00:01:00Z", "message": map[string]any{"role": kind, "content": text, "stop_reason": stop}})
		return string(raw) + "\n"
	}
	prefix := line(first, "", "user", "keep", "") + line(answer, first, "assistant", "answer", "end_turn")
	transcript := prefix + line(target, answer, "user", "edit", "") + line(later, target, "assistant", "discard", "end_turn")
	for _, tc := range []struct {
		name, body string
		target     protocol.ClaudeRewriteTarget
		want       string
		fail       bool
	}{
		{"native UUID", transcript, protocol.ClaudeRewriteTarget{MessageID: target}, answer, false},
		{"first message", transcript, protocol.ClaudeRewriteTarget{MessageID: first}, "", false},
		{"legacy exact match", transcript, protocol.ClaudeRewriteTarget{MessageID: uuid.NewString(), Prompt: "edit", StartedAt: "2026-11-06T00:00:00Z", Before: "2026-11-06T00:02:00Z"}, answer, false},
		{"ambiguous legacy", transcript + line(uuid.NewString(), later, "user", "edit", ""), protocol.ClaudeRewriteTarget{Prompt: "edit", StartedAt: "2026-11-06T00:00:00Z"}, "", true},
		{"outside interval", transcript, protocol.ClaudeRewriteTarget{Prompt: "edit", StartedAt: "2026-11-06T00:02:00Z"}, "", true},
		{"assistant target", transcript, protocol.ClaudeRewriteTarget{MessageID: answer}, "", true},
		{"broken chain", line(target, answer, "user", "edit", ""), protocol.ClaudeRewriteTarget{MessageID: target}, "", true},
		{"unfinished tool", line(first, "", "user", "keep", "") + line(answer, first, "assistant", "tool", "tool_use") + line(target, answer, "user", "edit", ""), protocol.ClaudeRewriteTarget{MessageID: target}, "", true},
		{"bad json", transcript + "{broken", protocol.ClaudeRewriteTarget{MessageID: target}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := rewriteBoundary(context.Background(), path, tc.target)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("boundary=%q err=%v", got, err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != tc.body {
				t.Fatal("modified the transcript")
			}
		})
	}
}

func TestPrepareRewriteKeepsSourceAndUsesImmutableSnapshot(t *testing.T) {
	config, state := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	session, first, answer, target := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	dir := filepath.Join(config, "projects", "project")
	os.MkdirAll(dir, 0700)
	content := `{"type":"user","uuid":"` + first + `","message":{"role":"user","content":"keep"}}` + "\n" +
		`{"type":"assistant","uuid":"` + answer + `","parentUuid":"` + first + `","message":{"role":"assistant","content":"answer","stop_reason":"end_turn"}}` + "\n" +
		`{"type":"user","uuid":"` + target + `","parentUuid":"` + answer + `","message":{"role":"user","content":"old"}}` + "\n"
	path := filepath.Join(dir, session+".jsonl")
	os.WriteFile(path, []byte(content), 0600)
	c := protocol.ClaudeTurnCommand{SessionID: session, Rewrite: &protocol.ClaudeRewriteTarget{MessageID: target}}
	if err := PrepareRewrite(context.Background(), state, uuid.NewString(), &c); err != nil {
		t.Fatal(err)
	}
	if c.SessionID != "" || c.ForkSessionID != session || c.ResumeAt != answer || c.ResumePath == "" {
		t.Fatalf("bad resume: %+v", c)
	}
	after, _ := os.ReadFile(path)
	snapshot, _ := os.ReadFile(c.ResumePath)
	if string(after) != content || string(snapshot) != content {
		t.Fatal("source or snapshot changed")
	}
}
