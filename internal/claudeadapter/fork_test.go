package claudeadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRemoveSnapshotAndPruneSnapshots(t *testing.T) {
	state := t.TempDir()
	id1 := "550e8400-e29b-41d4-a716-446655440010"
	id2 := "550e8400-e29b-41d4-a716-446655440020"

	p1, err := SnapshotPath(state, id1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := SnapshotPath(state, id2)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(p1), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p1, []byte("data1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, []byte("data2"), 0600); err != nil {
		t.Fatal(err)
	}

	// RemoveSnapshot removes p1
	if err := RemoveSnapshot(state, id1); err != nil {
		t.Fatalf("RemoveSnapshot: %v", err)
	}
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Fatalf("expected p1 to be deleted, stat err: %v", err)
	}
	// Calling RemoveSnapshot again on non-existent file returns nil
	if err := RemoveSnapshot(state, id1); err != nil {
		t.Fatalf("second RemoveSnapshot should return nil, got: %v", err)
	}

	// Make p2 old (10 days old)
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(p2, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Create p3 fresh (now)
	id3 := "550e8400-e29b-41d4-a716-446655440030"
	p3, _ := SnapshotPath(state, id3)
	if err := os.WriteFile(p3, []byte("data3"), 0600); err != nil {
		t.Fatal(err)
	}

	// Prune with 7-day retention
	if err := PruneSnapshots(state, 7*24*time.Hour, time.Now()); err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	// p2 should be gone
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Fatalf("expected p2 (old) to be pruned, got %v", err)
	}
	// p3 should still exist
	if _, err := os.Stat(p3); err != nil {
		t.Fatalf("expected p3 (fresh) to remain, got %v", err)
	}
}
