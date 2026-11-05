package claudeadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

func runSteeringTestChild(mode string) {
	r := bufio.NewReader(os.Stdin)
	initial, _ := r.ReadBytes('\n')
	var first struct {
		UUID string `json:"uuid"`
	}
	_ = json.Unmarshal(initial, &first)
	fmt.Printf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q}\n", nativeSession)
	line, _ := r.ReadBytes('\n')
	var input struct {
		Type, UUID, Priority string
		Origin               struct{ Kind string }
		Message              struct{ Content json.RawMessage }
	}
	if json.Unmarshal(line, &input) != nil || input.Type != "user" || input.Priority != "now" || input.Origin.Kind != "human" {
		os.Exit(2)
	}
	_ = os.WriteFile(os.Getenv("WIO_CLAUDE_TEST_RECORD"), line, 0600)
	// A result from the interrupted generation can arrive before the new
	// instruction's receipt. It must not close the live input stream.
	fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"queued_turn_count\":1,\"user_message_uuid\":%q}\n", first.UUID)
	if mode == "steer-no-receipt" {
		return
	}
	fmt.Printf("{\"type\":\"user\",\"isReplay\":true,\"uuid\":%q,\"message\":{\"content\":\"correction\"}}\n", input.UUID)
	fmt.Printf("{\"type\":\"stream_event\",\"user_message_uuids\":[%q,%q],\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Adjusted\"}}}\n", first.UUID, input.UUID)
	fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"queued_turn_count\":0,\"user_message_uuids\":[%q,%q]}\n", first.UUID, input.UUID)
	_, _ = io.ReadAll(r) // Exit only after the adapter closes stdin at the final result.
}

func TestClaudeLiveSteeringReceiptsAndMultipleResults(t *testing.T) {
	t.Setenv("WIO_CLAUDE_TEST_CHILD", "steer-success")
	record := filepath.Join(t.TempDir(), "input.json")
	t.Setenv("WIO_CLAUDE_TEST_RECORD", record)
	binary, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sessions Sessions
	ready := make(chan struct{})
	done := make(chan error, 1)
	var kinds []string
	go func() {
		done <- sessions.Run(ctx, binary, "operation", protocol.ClaudeTurnCommand{ThreadID: "thread", Workspace: t.TempDir(), Prompt: "initial", PermissionMode: "default", Steer: true}, func(e protocol.StreamEvent) error {
			kinds = append(kinds, e.Kind)
			if e.Kind == "claude.steer.ready" {
				close(ready)
			}
			return nil
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("input never became ready")
	}
	command := protocol.ClaudeSteerCommand{ThreadID: "thread", OperationID: "operation", RequestID: uuid.NewString(), Prompt: "change direction"}
	if err := sessions.Steer(ctx, command); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(kinds, ",")
	for _, kind := range []string{"claude.steer.accepted", "claude.steer.applied", "claude.steer.closed"} {
		if strings.Count(joined, kind) != 1 {
			t.Fatal("wrong receipt sequence", joined)
		}
	}
	if err := sessions.Steer(ctx, command); err == nil || err.Error() != protocol.ClaudeSteerUnconfirmedMessage {
		t.Fatal("missing live receipt ledger must leave delivery uncertain", err)
	}
	raw, _ := os.ReadFile(record)
	if !strings.Contains(string(raw), command.RequestID) {
		t.Fatal("missing message UUID")
	}
}

type capturedInput struct {
	mu     sync.Mutex
	writes int
	raw    []byte
}

func (w *capturedInput) Write(raw []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	w.raw = append(w.raw, raw...)
	return len(raw), nil
}
func (w *capturedInput) Close() error { return nil }

func TestClaudeLiveSteeringDeduplicatesAndChecksOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pipe := &capturedInput{}
	live := &liveTurn{threadID: "thread", operationID: "operation", ready: true, stdin: pipe, cancel: cancel, inputs: map[string]*liveInput{}, emit: func(string, map[string]any) error { return nil }}
	sessions := Sessions{turns: map[string]*liveTurn{"operation": live}}
	c := protocol.ClaudeSteerCommand{ThreadID: "thread", OperationID: "operation", RequestID: uuid.NewString(), Prompt: "correction"}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- sessions.Steer(ctx, c) }()
	}
	for {
		live.mu.Lock()
		present := live.inputs[c.RequestID] != nil
		live.mu.Unlock()
		if present {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("no live input")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := live.acknowledge(c.RequestID, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	pipe.mu.Lock()
	writes := pipe.writes
	pipe.mu.Unlock()
	if writes != 1 {
		t.Fatal("duplicate native input", writes)
	}
	changed := c
	changed.Prompt = "different"
	if sessions.Steer(ctx, changed) == nil {
		t.Fatal("reused ID with different content")
	}
	changed = c
	changed.ThreadID = "another"
	if sessions.Steer(ctx, changed) == nil {
		t.Fatal("steered another thread")
	}
	changed = c
	changed.OperationID = "old"
	if sessions.Steer(ctx, changed) == nil {
		t.Fatal("steered another operation")
	}
	if err := live.result(0); err != nil {
		t.Fatal(err)
	} // A received prompt is not yet applied.
	live.mu.Lock()
	closed := live.closed
	live.mu.Unlock()
	if closed {
		t.Fatal("closed before pending input was used")
	}
	live.finish()
}

func TestClaudeLiveSteeringWaitsForBackgroundTasks(t *testing.T) {
	var kinds []string
	live := &liveTurn{ready: true, stdin: &capturedInput{}, inputs: map[string]*liveInput{}, emit: func(kind string, _ map[string]any) error {
		kinds = append(kinds, kind)
		return nil
	}}
	if err := live.backgroundTasks(1); err != nil {
		t.Fatal(err)
	}
	if err := live.result(0); err != nil || live.closed {
		t.Fatal("closed while a background tool still owns the workspace", err)
	}
	if err := live.backgroundTasks(0); err != nil || !live.closed {
		t.Fatal("did not close after the final background tool finished", err)
	}
	if strings.Join(kinds, ",") != "steer.closed" {
		t.Fatal("unexpected receipts", kinds)
	}
}

func TestClaudeLiveSteeringExitWithoutReceiptIsUnconfirmed(t *testing.T) {
	t.Setenv("WIO_CLAUDE_TEST_CHILD", "steer-no-receipt")
	t.Setenv("WIO_CLAUDE_TEST_RECORD", filepath.Join(t.TempDir(), "input.json"))
	binary, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sessions Sessions
	ready := make(chan struct{})
	done := make(chan error, 1)
	var kinds []string
	go func() {
		done <- sessions.Run(ctx, binary, "operation", protocol.ClaudeTurnCommand{ThreadID: "thread", Workspace: t.TempDir(), Prompt: "initial", PermissionMode: "default", Steer: true}, func(e protocol.StreamEvent) error {
			kinds = append(kinds, e.Kind)
			if e.Kind == "claude.steer.ready" {
				close(ready)
			}
			return nil
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("input never became ready")
	}
	err := sessions.Steer(ctx, protocol.ClaudeSteerCommand{ThreadID: "thread", OperationID: "operation", RequestID: uuid.NewString(), Prompt: "correction"})
	if err == nil || err.Error() != protocol.ClaudeSteerUnconfirmedMessage {
		t.Fatal("lost receipt was not reported as uncertain", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(kinds, ",")
	if strings.Count(joined, "claude.steer.unconfirmed") != 1 || strings.Contains(joined, "claude.steer.applied") {
		t.Fatal("incorrect delivery claim", joined)
	}
}
