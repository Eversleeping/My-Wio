package geminiadapter

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

func TestVersionSwitchReservesAdapterWithoutBlockingHeartbeat(t *testing.T) {
	a := New("original", nil)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- a.ReconfigureCommand("new", func() error { close(started); <-release; return errors.New("failed install") })
	}()
	<-started
	if a.Command() != "original" {
		t.Fatal("command changed during installation")
	}
	if err := a.Run(context.Background(), "blocked", protocol.GeminiTurnCommand{}); err == nil {
		t.Fatal("turn started during installation")
	}
	close(release)
	if err := <-done; err == nil || a.Command() != "original" {
		t.Fatal("failed installation changed command")
	}
	a.active["running"] = func() {}
	called := false
	if err := a.ReconfigureCommand("new", func() error { called = true; return nil }); err == nil || called {
		t.Fatal("active turn did not block installation")
	}
}

// Re-execute the test binary as a portable fake CLI, including on Windows.
func init() {
	mode := os.Getenv("WIO_GEMINI_TEST_CHILD")
	if mode == "" {
		return
	}
	stdin, _ := io.ReadAll(os.Stdin)
	thinkingSettings, _ := os.ReadFile(os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH"))
	if len(thinkingSettings) == 0 {
		thinkingSettings = []byte("null")
	}
	raw, _ := json.Marshal(map[string]any{"thinkingPath": os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH"), "thinkingSettings": json.RawMessage(thinkingSettings), "args": os.Args[1:], "stdin": string(stdin), "dir": mustWorkingDir(), "apiKey": os.Getenv("GEMINI_API_KEY"), "endpoint": os.Getenv("GOOGLE_GEMINI_BASE_URL"), "trust": os.Getenv("GEMINI_CLI_TRUST_WORKSPACE")})
	_ = os.WriteFile(os.Getenv("WIO_GEMINI_TEST_RECORD"), raw, 0600)
	if mode == "untrusted" {
		fmt.Fprintln(os.Stderr, "private-provider-diagnostic")
		os.Exit(55)
	}
	fmt.Println(`{"type":"init","session_id":"native-session","model":"flash"}`)
	if mode == "wait" {
		time.Sleep(time.Hour)
		os.Exit(1)
	}
	if mode == "invalid" {
		fmt.Println("not json")
		os.Exit(0)
	}
	fmt.Println(`{"type":"message","role":"user","content":"duplicate"}`)
	fmt.Println(`{"type":"message","role":"assistant","content":"hello","delta":true}`)
	fmt.Println(`{"type":"tool_use","tool_id":"tool-1","tool_name":"read_file","parameters":{"path":"README.md"}}`)
	fmt.Println(`{"type":"tool_result","tool_id":"tool-1","status":"success","output":"file content"}`)
	if mode != "missing-result" {
		fmt.Println(`{"type":"result","status":"success","stats":{"input_tokens":10,"output_tokens":2}}`)
	}
	os.Exit(0)
}

func mustWorkingDir() string { dir, _ := os.Getwd(); return dir }

func TestWorkspaceTrustIsProcessScopedAndPreservesApprovalMode(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "success")
	t.Setenv("GEMINI_CLI_TRUST_WORKSPACE", "false")
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_GEMINI_TEST_RECORD", record)
	binary, _ := os.Executable()
	a := New(binary, func(protocol.StreamEvent) error { return nil })
	for _, mode := range []string{"default", "auto_edit"} {
		command := protocol.GeminiTurnCommand{Workspace: t.TempDir(), Model: "flash", ApprovalMode: mode, Prompt: "test"}
		for _, trusted := range []bool{true, false} {
			run := a.Run
			want := "false"
			if trusted {
				run, want = a.RunInTrustedWorkspace, "true"
			}
			if err := run(context.Background(), "trust", command); err != nil {
				t.Fatal(err)
			}
			var captured struct {
				Args  []string
				Trust string
			}
			raw, _ := os.ReadFile(record)
			if json.Unmarshal(raw, &captured) != nil || captured.Trust != want || !strings.Contains(strings.Join(captured.Args, " "), "--approval-mode "+mode) {
				t.Fatalf("unexpected workspace configuration: %+v", captured)
			}
			if os.Getenv("GEMINI_CLI_TRUST_WORKSPACE") != "false" {
				t.Fatal("workspace trust escaped CLI process")
			}
		}
	}
}

func TestUntrustedWorkspaceErrorDoesNotExposeStderr(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "untrusted")
	t.Setenv("WIO_GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "record.json"))
	binary, _ := os.Executable()
	a := New(binary, func(protocol.StreamEvent) error { return nil })
	err := a.Run(context.Background(), "untrusted", protocol.GeminiTurnCommand{Workspace: t.TempDir(), Model: "flash", ApprovalMode: "default", Prompt: "test"})
	if err == nil || !strings.Contains(err.Error(), "untrusted workspace") || strings.Contains(err.Error(), "private-provider-diagnostic") {
		t.Fatalf("incorrect trust diagnostic: %v", err)
	}
}

func TestRunStreamsAndResumesWithoutPuttingPromptInArguments(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "success")
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_GEMINI_TEST_RECORD", record)
	binary, _ := os.Executable()
	var events []protocol.StreamEvent
	a := New(binary, func(event protocol.StreamEvent) error { events = append(events, event); return nil })
	workspace := t.TempDir()
	err := a.Run(context.Background(), "operation-1", protocol.GeminiTurnCommand{ThreadID: "thread-1", Workspace: workspace, SessionID: "previous-session", Prompt: "private prompt", Model: "flash", ApprovalMode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events: %#v", events)
	}
	for _, event := range events {
		var p map[string]any
		_ = json.Unmarshal(event.Payload, &p)
		if p["operation_id"] != "operation-1" || event.StreamID != "thread-1" {
			t.Fatalf("missing routing: %#v", event)
		}
	}
	var captured struct {
		Args       []string
		Stdin, Dir string
	}
	raw, _ := os.ReadFile(record)
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(captured.Args, " ")
	if strings.Contains(args, "private prompt") || !strings.Contains(args, "--resume previous-session") || !strings.Contains(args, "--output-format stream-json") {
		t.Fatal(args)
	}
	if captured.Stdin != "private prompt" || !strings.EqualFold(captured.Dir, workspace) {
		t.Fatalf("capture: %+v", captured)
	}
}

func TestRunRejectsTruncatedAndInvalidStreams(t *testing.T) {
	for _, mode := range []string{"missing-result", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WIO_GEMINI_TEST_CHILD", mode)
			t.Setenv("WIO_GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "record"))
			binary, _ := os.Executable()
			a := New(binary, func(protocol.StreamEvent) error { return nil })
			err := a.Run(context.Background(), mode, protocol.GeminiTurnCommand{Workspace: t.TempDir(), Model: "auto", ApprovalMode: "default", Prompt: "test"})
			if err == nil || err == context.Canceled {
				t.Fatalf("lost stream error: %v", err)
			}
		})
	}
}

func TestInterruptBeforeAndDuringStart(t *testing.T) {
	a := New("does-not-exist", nil)
	a.Interrupt("cancel-before-start")
	if err := a.Run(context.Background(), "cancel-before-start", protocol.GeminiTurnCommand{}); err != context.Canceled {
		t.Fatal(err)
	}
	t.Setenv("WIO_GEMINI_TEST_CHILD", "wait")
	t.Setenv("WIO_GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "record"))
	binary, _ := os.Executable()
	started := make(chan struct{}, 1)
	a = New(binary, func(protocol.StreamEvent) error { started <- struct{}{}; return nil })
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		done <- a.Run(ctx, "active", protocol.GeminiTurnCommand{Workspace: t.TempDir(), Model: "auto", ApprovalMode: "default", Prompt: "test"})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("CLI did not start")
	}
	called := false
	if err := a.ReconfigureEnvironment([]string{"GEMINI_API_KEY=new-secret"}, "pro", func() error { called = true; return nil }); err == nil || called {
		t.Fatal("active turn allowed credential change")
	}
	a.Interrupt("active")
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("interrupt: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("interrupt did not stop process")
	}
}

func TestAPIEnvironmentAndDefaultModelAreAppliedWithoutGlobalMutation(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "success")
	record := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("WIO_GEMINI_TEST_RECORD", record)
	t.Setenv("GEMINI_API_KEY", "inherited-secret")
	binary, _ := os.Executable()
	a := New(binary, func(protocol.StreamEvent) error { return nil })
	if err := a.ReconfigureEnvironment([]string{"GEMINI_API_KEY=configured-secret", "GOOGLE_GEMINI_BASE_URL=https://gemini.example.test"}, "flash", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := a.ReconfigureEnvironment([]string{"GEMINI_API_KEY=failed-secret"}, "pro", func() error { return errors.New("write failed") }); err == nil {
		t.Fatal("expected failure")
	}
	if err := a.Run(context.Background(), "configured", protocol.GeminiTurnCommand{Workspace: t.TempDir(), Model: "auto", ApprovalMode: "default", Prompt: "test"}); err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Args             []string
		APIKey, Endpoint string
	}
	raw, _ := os.ReadFile(record)
	if json.Unmarshal(raw, &captured) != nil || captured.APIKey != "configured-secret" || captured.Endpoint != "https://gemini.example.test" || !strings.Contains(strings.Join(captured.Args, " "), "--model flash") {
		t.Fatal("incorrect process configuration")
	}
	if os.Getenv("GEMINI_API_KEY") != "inherited-secret" {
		t.Fatal("global environment changed")
	}
}
