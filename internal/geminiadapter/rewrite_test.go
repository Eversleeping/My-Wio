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
	"strings"
	"testing"
	"time"
)

func nativeHistory(workspace string) []byte {
	hash := sha256.Sum256([]byte(workspace))
	metadata, _ := json.Marshal(map[string]string{"sessionId": "original-session", "projectHash": hex.EncodeToString(hash[:]), "startTime": "2026-10-25T00:00:00Z"})
	return append(append(metadata, '\n'), []byte(`{"id":"u1","type":"user","timestamp":"2026-10-25T00:00:01Z","content":"keep"}
{"id":"a1","type":"gemini","timestamp":"2026-10-25T00:00:02Z","content":"kept answer"}
{"$patch":{"id":"a1","content":"patched kept answer"}}
{"id":"u2","type":"user","timestamp":"2026-10-25T00:01:01Z","content":"discard"}
{"id":"a2","type":"gemini","timestamp":"2026-10-25T00:01:02Z","content":"discarded answer"}
{"$set":{"summary":"future summary must disappear"}}
`)...)
}

func TestRewritePrefixKeepsNativePatchesAndExcludesAllFutureContext(t *testing.T) {
	raw := nativeHistory("/project")
	hash := sha256.Sum256([]byte("/project"))
	at, _ := time.Parse(time.RFC3339, "2026-10-25T00:01:00Z")
	prefix, matched, err := rewritePrefix(raw, "original-session", hex.EncodeToString(hash[:]), at, time.Time{})
	if err != nil || !matched || !bytes.Contains(prefix, []byte(`"$patch"`)) || bytes.Contains(prefix, []byte("discard")) || bytes.Contains(prefix, []byte("future summary")) {
		t.Fatalf("incorrect prefix: %s %v", prefix, err)
	}
	first, _ := time.Parse(time.RFC3339, "2026-10-25T00:00:00Z")
	prefix, matched, err = rewritePrefix(raw, "original-session", hex.EncodeToString(hash[:]), first, at)
	if err != nil || !matched || bytes.Contains(prefix, []byte(`"type":"user"`)) {
		t.Fatalf("first-message rewrite did not create empty context: %s %v", prefix, err)
	}
	// A turn that failed before recording its user message must not select a
	// later turn's message by mistake.
	missing, _ := time.Parse(time.RFC3339, "2026-10-25T00:00:10Z")
	end, _ := time.Parse(time.RFC3339, "2026-10-25T00:00:20Z")
	if _, matched, err := rewritePrefix(raw, "original-session", hex.EncodeToString(hash[:]), missing, end); !matched || err == nil {
		t.Fatal("missing boundary selected a later turn")
	}
	if _, matched, _ := rewritePrefix(raw, "original-session", "another-project", at, time.Time{}); matched {
		t.Fatal("foreign project session accepted")
	}
}

func TestLegacyRewriteDropsFutureMetadata(t *testing.T) {
	raw := []byte(`{"sessionId":"native","projectHash":"project","summary":"future","memoryScratchpad":{"workflowSummary":"future"},"messages":[{"id":"u1","type":"user","timestamp":"2026-10-25T00:00:01Z","content":"keep"},{"id":"u2","type":"user","timestamp":"2026-10-25T00:01:01Z","content":"discard"}]}`)
	at, _ := time.Parse(time.RFC3339, "2026-10-25T00:01:00Z")
	prefix, matched, err := rewritePrefix(raw, "native", "project", at, time.Time{})
	if err != nil || !matched || !bytes.Contains(prefix, []byte("keep")) || bytes.Contains(prefix, []byte("future")) || bytes.Contains(prefix, []byte("discard")) {
		t.Fatalf("legacy prefix: %s %v", prefix, err)
	}
}

func TestRewriteUsesOfficialImportAndPreservesSource(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "success")
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_GEMINI_TEST_RECORD", record)
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	workspace := t.TempDir()
	chats := filepath.Join(home, ".gemini", "tmp", "project-short-id", "chats")
	if err := os.MkdirAll(chats, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(chats, "session-original.jsonl")
	raw := nativeHistory(workspace)
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	binary, _ := os.Executable()
	var events []protocol.StreamEvent
	a := New(binary, func(event protocol.StreamEvent) error { events = append(events, event); return nil })
	command := protocol.GeminiTurnCommand{ThreadID: "thread", Workspace: workspace, SessionID: "original-session", Prompt: "changed", Model: "flash", ApprovalMode: "default", Rewrite: &protocol.GeminiRewriteTarget{EventID: "u2", StartedAt: "2026-10-25T00:01:00Z"}}
	if err := a.Run(context.Background(), "rewrite", command); err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Args  []string
		Stdin string
	}
	data, _ := os.ReadFile(record)
	if err := json.Unmarshal(data, &captured); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(captured.Args, " ")
	if !strings.Contains(args, "--session-file") || strings.Contains(args, "--resume") || strings.Contains(args, "changed") || captured.Stdin != "changed" {
		t.Fatalf("unexpected invocation: %+v", captured)
	}
	for i, arg := range captured.Args {
		if arg == "--session-file" {
			if _, err := os.Stat(captured.Args[i+1]); !os.IsNotExist(err) {
				t.Fatal("temporary history file not cleaned")
			}
		}
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(raw, after) {
		t.Fatal("source native history modified")
	}
	if len(events) == 0 || events[0].Kind != "gemini.init" {
		t.Fatalf("missing imported session init: %+v", events)
	}
	command.Rewrite.StartedAt = "2099-01-01T00:00:00Z"
	events = nil
	if err := a.Run(context.Background(), "missing", command); err == nil || len(events) != 0 {
		t.Fatal("missing target launched CLI")
	}
}
