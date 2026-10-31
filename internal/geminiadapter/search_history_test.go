package geminiadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func searchHistoryRecord(id, output string, patch bool) []byte {
	entry := map[string]any{"toolCalls": []any{map[string]any{"id": id, "name": "grep_search", "resultDisplay": map[string]any{"summary": output, "matches": []any{}}, "result": []any{map[string]any{"functionResponse": map[string]any{"id": id}}}}}}
	if patch {
		entry = map[string]any{"$patch": entry}
	} else {
		entry["type"] = "gemini"
	}
	raw, _ := json.Marshal(entry)
	return append(raw, '\n')
}

func appendHistory(t *testing.T, path string, raw []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		t.Fatal(err)
	}
}

func TestSearchHistoryOnlyRecoversCompleteResultsFromThisTurn(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	chats := filepath.Join(home, ".gemini", "tmp", "project", "chats")
	if err := os.MkdirAll(chats, 0700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(workspace))
	header, _ := json.Marshal(map[string]string{"sessionId": "native", "projectHash": hex.EncodeToString(hash[:])})
	path := filepath.Join(chats, "session-native.jsonl")
	id := "grep_search__same-id"
	raw := append(append(header, '\n'), searchHistoryRecord(id, "old output", false)...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	h := newNativeSearchHistory("native", workspace, []string{"GEMINI_CLI_HOME=" + home}, true)
	if h == nil {
		t.Fatal("session was not found")
	}
	h.wait = false
	if _, ok := h.output(context.Background(), id); ok {
		t.Fatal("recovered a result from an earlier turn")
	}
	appendHistory(t, path, searchHistoryRecord("grep_search__another-id", "other output", false))
	if _, ok := h.output(context.Background(), id); ok {
		t.Fatal("recovered a different tool call")
	}
	current := searchHistoryRecord(id, "current output", true)
	appendHistory(t, path, current[:len(current)-1])
	if _, ok := h.output(context.Background(), id); ok {
		t.Fatal("accepted a partially written record")
	}
	appendHistory(t, path, []byte{'\n'})
	if output, ok := h.output(context.Background(), id); !ok || output != "current output" {
		t.Fatalf("current result: %q, %v", output, ok)
	}
	if _, ok := h.output(context.Background(), "run_shell_command__same-id"); ok {
		t.Fatal("used the search fallback for a shell command")
	}
	if h := newNativeSearchHistory("native", t.TempDir(), []string{"GEMINI_CLI_HOME=" + home}, true); h != nil {
		t.Fatal("accepted history from a different workspace")
	}
	if h := newNativeSearchHistory("other-session", workspace, []string{"GEMINI_CLI_HOME=" + home}, true); h != nil {
		t.Fatal("accepted history from a different session")
	}
	if err := os.WriteFile(filepath.Join(chats, "session-duplicate.jsonl"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if h := newNativeSearchHistory("native", workspace, []string{"GEMINI_CLI_HOME=" + home}, true); h != nil {
		t.Fatal("accepted ambiguous session histories")
	}
}
