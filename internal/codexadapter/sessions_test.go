package codexadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// The test binary doubles as a JSONL app-server. It deliberately queues new
// conversations while a turn is active, and reuses approval IDs across
// processes, to exercise isolation through the real subprocess transport.
func init() {
	if os.Getenv("WIO_TEST_SESSION_SERVER") != "1" {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-cli test")
		os.Exit(0)
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	active := false
	loaded := false
	threadID := "helper-thread"
	var queued []rpcMessage
	respond := func(id json.RawMessage, result any) { _ = encoder.Encode(map[string]any{"id": id, "result": result}) }
	notify := func(method string, params any) {
		_ = encoder.Encode(map[string]any{"method": method, "params": params})
	}
	for scanner.Scan() {
		var message rpcMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		switch message.Method {
		case "initialize":
			respond(message.ID, map[string]any{})
		case "thread/start":
			if active {
				queued = append(queued, message)
				continue
			}
			loaded = true
			respond(message.ID, map[string]any{"thread": map[string]string{"id": threadID}})
		case "thread/resume":
			threadID = findString(message.Params, "threadId")
			loaded = true
			respond(message.ID, map[string]any{"thread": map[string]string{"id": threadID}})
		case "thread/fork":
			threadID = "helper-fork"
			loaded = true
			respond(message.ID, map[string]any{"thread": map[string]string{"id": threadID}})
		case "turn/start":
			active = true
			respond(message.ID, map[string]any{"turn": map[string]string{"id": "helper-turn"}})
			notify("turn/started", map[string]any{"threadId": threadID, "turn": map[string]string{"id": "helper-turn"}})
			_ = encoder.Encode(map[string]any{"id": 42, "method": "item/commandExecution/requestApproval", "params": map[string]string{"threadId": threadID, "turnId": "helper-turn", "itemId": "helper-command"}})
		case "turn/interrupt":
			active = false
			respond(message.ID, map[string]any{})
			notify("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]string{"id": "helper-turn", "status": "interrupted"}})
			for _, waiting := range queued {
				respond(waiting.ID, map[string]any{"thread": map[string]string{"id": threadID}})
			}
			queued = nil
		case "thread/goal/get":
			if !loaded {
				_ = encoder.Encode(map[string]any{"id": message.ID, "error": map[string]any{"code": -32600, "message": "thread not loaded"}})
				continue
			}
			respond(message.ID, map[string]any{"goal": map[string]string{"threadId": threadID, "objective": fmt.Sprintf("process:%d", os.Getpid())}})
		case "":
			if idKey(message.ID) == "42" {
				notify("item/completed", map[string]any{"threadId": threadID, "item": map[string]string{"id": "approval-handled", "type": "commandExecution"}})
			}
		}
	}
	os.Exit(0)
}

func sessionTestPool(t *testing.T) (*Sessions, chan protocol.StreamEvent) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan protocol.StreamEvent, 64)
	p := NewSessions(executable, []string{"WIO_TEST_SESSION_SERVER=1"}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(event protocol.StreamEvent) error {
		events <- event
		return nil
	})
	t.Cleanup(func() { _ = p.Close() })
	return p, events
}

func waitSessionEvent(t *testing.T, events <-chan protocol.StreamEvent, threadID, kind string) protocol.StreamEvent {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.StreamID == threadID && event.Kind == kind {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s in %s", kind, threadID)
		}
	}
}

func TestSessionsRunParallelInSameWorkspaceAndRouteControls(t *testing.T) {
	p, events := sessionTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, id := range []string{"wio-a", "wio-b"} {
		if err := p.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: id, Workspace: "/same/workspace", Prompt: "test"}); err != nil {
			t.Fatal(err)
		}
		waitSessionEvent(t, events, id, "approval.requested")
	}
	a := p.sessions["wio-a"].adapter
	b := p.sessions["wio-b"].adapter
	if a.process.Process.Pid == b.process.Process.Pid || !a.hasActiveTurn() || !b.hasActiveTurn() {
		t.Fatal("both conversations must be active in separate processes")
	}
	if err := p.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: "wio-a", CodexThread: "helper-thread", Prompt: "duplicate"}); err == nil {
		t.Fatal("a second turn in the same session must be rejected")
	}
	for _, id := range []string{"wio-b", "wio-a"} {
		if err := p.Decide(protocol.ApprovalDecisionCommand{ThreadID: id, RequestID: "42", Decision: "approved"}); err != nil {
			t.Fatal(err)
		}
		waitSessionEvent(t, events, id, "codex.item.completed")
	}
	if err := p.Interrupt(ctx, protocol.InterruptTurnCommand{ThreadID: "wio-a"}); err != nil {
		t.Fatal(err)
	}
	waitSessionEvent(t, events, "wio-a", "codex.turn.completed")
	if a.hasActiveTurn() || !b.hasActiveTurn() {
		t.Fatal("interrupting one session affected the other session")
	}
	called := false
	if err := p.ReconfigureEnvironment(nil, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("an active session must block credential updates before files change")
	}
	if err := p.ReconfigureCommand("updated-codex", func() error { called = true; return nil }); err == nil || called {
		t.Fatal("an active session must block CLI updates before files change")
	}
	if err := p.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: "wio-a", CodexThread: "helper-thread", Prompt: "continue"}); err != nil {
		t.Fatal(err)
	}
	if p.sessions["wio-a"].adapter != a {
		t.Fatal("continuing a session should reuse its process")
	}
	a.mu.Lock()
	process := a.process.Process
	a.mu.Unlock()
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitSessionEvent(t, events, "wio-a", "codex.turn.failed")
	if !b.hasActiveTurn() {
		t.Fatal("one app-server failure stopped another session")
	}
}

func TestSessionsForkUsesTargetProcessWhileSourceRuns(t *testing.T) {
	p, events := sessionTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: "source", Prompt: "test"}); err != nil {
		t.Fatal(err)
	}
	waitSessionEvent(t, events, "source", "approval.requested")
	fork, err := p.ForkThread(ctx, protocol.ForkThreadCommand{SourceThreadID: "source", TargetThreadID: "target", CodexThread: "helper-thread", Workspace: "/same/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: "target", CodexThread: fork.CodexThread, Prompt: "continue"}); err != nil {
		t.Fatal(err)
	}
	waitSessionEvent(t, events, "target", "approval.requested")
	if p.sessions["source"].adapter == p.sessions["target"].adapter || !p.sessions["source"].adapter.hasActiveTurn() || !p.sessions["target"].adapter.hasActiveTurn() {
		t.Fatal("a fork must run independently of its source session")
	}
}

func TestSessionsReclaimOnlyIdleProcessesAndResumeHistory(t *testing.T) {
	p, events := sessionTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, id := range []string{"idle", "active"} {
		if err := p.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: id, Prompt: "test"}); err != nil {
			t.Fatal(err)
		}
		waitSessionEvent(t, events, id, "approval.requested")
	}
	if err := p.Interrupt(ctx, protocol.InterruptTurnCommand{ThreadID: "idle"}); err != nil {
		t.Fatal(err)
	}
	waitSessionEvent(t, events, "idle", "codex.turn.completed")
	p.mu.Lock()
	for _, s := range p.sessions {
		s.lastUsed = time.Now().Add(-2 * sessionIdleTimeout)
	}
	old := p.sessions["idle"].adapter
	p.mu.Unlock()
	p.reapIdle(time.Now())
	if p.sessions["idle"] != nil || p.sessions["active"] == nil {
		t.Fatal("idle cleanup must preserve a running session")
	}
	old.mu.Lock()
	stopped := old.process == nil
	old.mu.Unlock()
	if !stopped {
		t.Fatal("idle process was not closed")
	}
	payload, _ := json.Marshal(protocol.CodexSnapshotCommand{ThreadID: "idle", CodexThread: "persisted-thread", Workspace: "/same/workspace"})
	result, err := p.CodexOperation(ctx, "codex.goal.get", payload)
	if err != nil || !result.Supported || !strings.Contains(string(result.Data), "persisted-thread") {
		t.Fatalf("a reclaimed session must resume before querying a goal: %#v %v", result, err)
	}
	if err := p.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: "idle", CodexThread: "persisted-thread", Prompt: "continue history"}); err != nil {
		t.Fatal(err)
	}
	accepted := waitSessionEvent(t, events, "idle", "turn.accepted")
	if !strings.Contains(string(accepted.Payload), "persisted-thread") {
		t.Fatalf("continuation lost its persisted thread: %s", accepted.Payload)
	}
}

func TestSessionsReconfigureAllIdleProcessesAndClose(t *testing.T) {
	p, _ := sessionTestPool(t)
	a, release, err := p.acquire("idle")
	if err != nil {
		t.Fatal(err)
	}
	release()
	updates := 0
	if err := p.ReconfigureEnvironment([]string{"NEW=value"}, func() error { updates++; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(p.sessions) != 0 || updates != 1 {
		t.Fatal("credential update must run once and invalidate idle sessions")
	}
	b, release, err := p.acquire("idle")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if b == a || len(b.environment) != 1 || b.environment[0] != "NEW=value" {
		t.Fatal("new sessions did not inherit updated credentials")
	}
	if err := p.ReconfigureCommand("new-codex", func() error { updates++; return nil }); err != nil {
		t.Fatal(err)
	}
	c, release, err := p.acquire("idle")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if c.command != "new-codex" || updates != 2 {
		t.Fatal("new sessions did not inherit updated CLI path")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.acquire("after-close"); err == nil {
		t.Fatal("closed pool accepted a new session")
	}
}
