package claudeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

const nativeSession = "550e8400-e29b-41d4-a716-446655440000"

func init() {
	mode := os.Getenv("WIO_CLAUDE_TEST_CHILD")
	if mode == "" {
		return
	}
	stdin, _ := io.ReadAll(os.Stdin)
	raw, _ := json.Marshal(map[string]any{"args": os.Args[1:], "stdin": string(stdin)})
	_ = os.WriteFile(os.Getenv("WIO_CLAUDE_TEST_RECORD"), raw, 0600)
	if mode == "invalid" {
		fmt.Println("not json")
		os.Exit(0)
	}
	if mode == "stderr" {
		fmt.Fprintln(os.Stderr, "private-provider-diagnostic")
		os.Exit(1)
	}
	session := nativeSession
	if mode == "wrong-session" {
		session = "550e8400-e29b-41d4-a716-446655440001"
	}
	fmt.Printf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q}\n", session)
	if mode == "wait" {
		time.Sleep(time.Hour)
		os.Exit(1)
	}
	fmt.Println(`{"type":"user","message":{"content":"the original prompt"}}`)
	fmt.Println(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}`)
	fmt.Println(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}}`)
	fmt.Println(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"README.md"}}]}}`)
	fmt.Println(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"file contents"}]}}`)
	fmt.Println(`{"type":"assistant","parent_tool_use_id":"other","message":{"content":[{"type":"text","text":"nested"}]}}`)
	if mode == "failed" {
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["model unavailable"]}`)
	} else if mode != "missing-result" {
		fmt.Println(`{"type":"result","subtype":"success","is_error":false,"usage":{"input_tokens":3,"output_tokens":2},"permission_denials":[]}`)
	}
	os.Exit(0)
}

func TestRunStreamsResumesAndKeepsPromptOffArguments(t *testing.T) {
	t.Setenv("WIO_CLAUDE_TEST_CHILD", "success")
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_CLAUDE_TEST_RECORD", record)
	binary, _ := os.Executable()
	var events []protocol.StreamEvent
	command := protocol.ClaudeTurnCommand{ThreadID: "thread", Workspace: t.TempDir(), SessionID: nativeSession, Prompt: "private prompt", Model: "sonnet", PermissionMode: "acceptEdits"}
	if err := Run(context.Background(), binary, "operation", command, func(event protocol.StreamEvent) error { events = append(events, event); return nil }); err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Args  []string
		Stdin string
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(captured.Args, " ")
	if captured.Stdin != "private prompt" || strings.Contains(args, "private prompt") || !strings.Contains(args, "--resume "+nativeSession) || !strings.Contains(args, "--permission-mode acceptEdits") || !strings.Contains(args, "--include-partial-messages") {
		t.Fatalf("incorrect invocation: %+v", captured)
	}
	var text, kinds string
	for _, event := range events {
		var payload map[string]any
		if json.Unmarshal(event.Payload, &payload) != nil || payload["operation_id"] != "operation" || event.StreamID != "thread" {
			t.Fatalf("bad routing: %+v", event)
		}
		kinds += event.Kind + " "
		if event.Kind == "claude.message" {
			text += payload["text"].(string)
		}
	}
	if text != "Hello world" || !strings.Contains(kinds, "claude.tool_use") || !strings.Contains(kinds, "claude.tool_result") || !strings.Contains(kinds, "claude.result") {
		t.Fatalf("unexpected stream: %q %s", text, kinds)
	}
}

func TestRunRejectsInvalidOrIncompleteCLIResults(t *testing.T) {
	binary, _ := os.Executable()
	for _, mode := range []string{"invalid", "missing-result", "failed", "wrong-session", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WIO_CLAUDE_TEST_CHILD", mode)
			t.Setenv("WIO_CLAUDE_TEST_RECORD", filepath.Join(t.TempDir(), "record"))
			err := Run(context.Background(), binary, "op", protocol.ClaudeTurnCommand{Workspace: t.TempDir(), SessionID: nativeSession, Prompt: "test", PermissionMode: "default"}, func(protocol.StreamEvent) error { return nil })
			if err == nil || strings.Contains(err.Error(), "private-provider-diagnostic") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRunWaitsForProcessExitOnCancellation(t *testing.T) {
	t.Setenv("WIO_CLAUDE_TEST_CHILD", "wait")
	t.Setenv("WIO_CLAUDE_TEST_RECORD", filepath.Join(t.TempDir(), "record"))
	binary, _ := os.Executable()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, binary, "op", protocol.ClaudeTurnCommand{Workspace: t.TempDir(), Prompt: "test", PermissionMode: "default"}, func(event protocol.StreamEvent) error {
			if event.Kind == "claude.init" {
				close(started)
			}
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("CLI did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CLI did not exit")
	}
}

func TestRunEffortArguments(t *testing.T) {
	binary, _ := os.Executable()
	for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max", "ultra", "--help"} {
		t.Run("effort="+effort, func(t *testing.T) {
			t.Setenv("WIO_CLAUDE_TEST_CHILD", "success")
			record := filepath.Join(t.TempDir(), "record.json")
			t.Setenv("WIO_CLAUDE_TEST_RECORD", record)
			err := Run(context.Background(), binary, "op", protocol.ClaudeTurnCommand{Workspace: t.TempDir(), PermissionMode: "default", Effort: effort}, func(protocol.StreamEvent) error { return nil })
			if effort == "ultra" || effort == "--help" {
				if err == nil {
					t.Fatal("invalid effort accepted")
				}
				if _, statErr := os.Stat(record); !os.IsNotExist(statErr) {
					t.Fatal("invalid effort launched CLI")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var captured struct{ Args []string }
			raw, _ := os.ReadFile(record)
			if err := json.Unmarshal(raw, &captured); err != nil {
				t.Fatal(err)
			}
			args := strings.Join(captured.Args, " ")
			if effort == "" && strings.Contains(args, "--effort") || effort != "" && !strings.Contains(args, "--effort "+effort) {
				t.Fatalf("wrong effort arguments: %s", args)
			}
		})
	}
}
