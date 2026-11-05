package claudeadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestSnapshotIsImmutableAndIdempotent(t *testing.T) {
	config, state := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	source := filepath.Join(config, "projects", "project", nativeSession+".jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\"sessionId\":\"" + nativeSession + "\",\"message\":\"before\"}\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	c := protocol.ClaudeForkCommand{SessionID: nativeSession, TargetThreadID: "550e8400-e29b-41d4-a716-446655440002"}
	r, err := Snapshot(context.Background(), state, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Snapshot(context.Background(), state, c); err != nil {
		t.Fatal(err)
	}
	target, _ := SnapshotPath(state, r.SnapshotID)
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != string(original) {
		t.Fatalf("snapshot changed: %s %v", raw, err)
	}
	for _, id := range []string{"../session", "", "not-a-uuid"} {
		if _, err := SnapshotPath(state, id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}

func TestRunImageInputAndForkArguments(t *testing.T) {
	t.Setenv("WIO_CLAUDE_TEST_CHILD", "success")
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_CLAUDE_TEST_RECORD", record)
	binary, _ := os.Executable()
	c := protocol.ClaudeTurnCommand{ThreadID: "target", Workspace: t.TempDir(), Prompt: "private image question", PermissionMode: "default", ResumePath: "/safe/snapshot.jsonl", ForkSessionID: "550e8400-e29b-41d4-a716-446655440002", Images: []protocol.TurnImage{{DataURL: "data:image/png;base64,aGVsbG8="}}}
	if err := Run(context.Background(), binary, "op", c, func(protocol.StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Args  []string
		Stdin string
	}
	if json.Unmarshal(raw, &captured) != nil {
		t.Fatal("bad record")
	}
	args := strings.Join(captured.Args, " ")
	if !strings.Contains(args, "--resume /safe/snapshot.jsonl --fork-session") || !strings.Contains(args, "--input-format stream-json") || strings.Contains(args, c.Prompt) {
		t.Fatalf("wrong args: %s", args)
	}
	var input struct {
		Type    string
		Message struct {
			Role    string
			Content []struct {
				Type   string
				Text   string
				Source struct {
					Type      string
					MediaType string `json:"media_type"`
					Data      string
				}
			}
		}
	}
	if json.Unmarshal([]byte(captured.Stdin), &input) != nil || input.Type != "user" || input.Message.Role != "user" || len(input.Message.Content) != 2 || input.Message.Content[1].Source.MediaType != "image/png" || input.Message.Content[1].Source.Data != "aGVsbG8=" {
		t.Fatalf("wrong image input: %s", captured.Stdin)
	}
	c.ForkSessionID = nativeSession
	if err := Run(context.Background(), binary, "op", c, func(protocol.StreamEvent) error { return nil }); err == nil {
		t.Fatal("fork reused source native ID")
	}
}
