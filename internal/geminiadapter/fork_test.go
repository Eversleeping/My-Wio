package geminiadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/wio-platform/wio/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestForkPreservesNativePatchesAndLeavesSourceIndependent(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	chats := filepath.Join(home, ".gemini", "tmp", "source-slug", "chats")
	if err := os.MkdirAll(chats, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(chats, "session-original.jsonl")
	original := nativeHistory(workspace)
	hash := sha256.Sum256([]byte(workspace))
	resumeMetadata, _ := json.Marshal(map[string]any{"$set": map[string]string{"sessionId": "original-session", "projectHash": hex.EncodeToString(hash[:])}})
	original = bytes.Replace(original, []byte(`{"id":"u2"`), append(append(resumeMetadata, '\n'), []byte(`{"id":"u2"`)...), 1)
	os.WriteFile(source, original, 0600)
	adapter := New("must-not-execute-model", nil)
	command := protocol.GeminiForkCommand{SessionID: "original-session", SourceWorkspace: workspace, Workspace: workspace, Before: &protocol.GeminiRewriteTarget{StartedAt: "2026-10-25T00:01:00Z"}}
	fork, err := adapter.Fork(context.Background(), "fork", command)
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(chats, "session-*-"+fork.SessionID[:8]+".jsonl"))
	if len(paths) != 1 {
		t.Fatal("native filename missing short session ID")
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if fork.SessionID == command.SessionID || !bytes.Contains(data, []byte("patched kept answer")) || bytes.Contains(data, []byte("discard")) || bytes.Contains(data, []byte("future summary")) {
		t.Fatalf("incorrect native fork: %s", data)
	}
	assertForkIdentity(t, data, fork.SessionID, hex.EncodeToString(hash[:]))
	after, _ := os.ReadFile(source)
	if !bytes.Equal(after, original) {
		t.Fatal("source history changed")
	}
	target := filepath.Join(t.TempDir(), "new-worktree")
	os.MkdirAll(target, 0700)
	command.Workspace = target
	command.Before = nil
	fork, err = adapter.Fork(context.Background(), "worktree-fork", command)
	if err != nil {
		t.Fatal(err)
	}
	hash = sha256.Sum256([]byte(target))
	paths, _ = filepath.Glob(filepath.Join(home, ".gemini", "tmp", hex.EncodeToString(hash[:]), "chats", "session-*-"+fork.SessionID[:8]+".jsonl"))
	if len(paths) != 1 {
		t.Fatal("worktree native history missing")
	}
	path := paths[0]
	data, err = os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("discarded answer")) || !bytes.Contains(data, []byte(hex.EncodeToString(hash[:]))) {
		t.Fatalf("worktree fork missing complete history: %v", err)
	}
	assertForkIdentity(t, data, fork.SessionID, hex.EncodeToString(hash[:]))
}

func assertForkIdentity(t *testing.T, raw []byte, sessionID, projectHash string) {
	t.Helper()
	// Gemini merges metadata in order, including $set updates from previous
	// resumes. Confirm that the effective identity belongs to this branch.
	metadata := map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		updates, isUpdate := record["$set"].(map[string]any)
		if !isUpdate {
			if record["sessionId"] == nil || record["projectHash"] == nil {
				continue
			}
			updates = record
		}
		for key, value := range updates {
			metadata[key] = value
		}
	}
	if metadata["sessionId"] != sessionID || metadata["projectHash"] != projectHash {
		t.Fatalf("native resume would load the source identity: %v", metadata)
	}
}
