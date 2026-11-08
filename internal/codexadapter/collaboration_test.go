package codexadapter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

// This opt-in smoke test starts a real CLI with an empty temporary CODEX_HOME.
// It discovers tools without submitting a model turn or using user credentials.
func TestLiveCodexCollaborationDiscovery(t *testing.T) {
	executable := os.Getenv("WIO_COLLABORATION_LIVE_CODEX")
	if executable == "" {
		t.Skip("set WIO_COLLABORATION_LIVE_CODEX to a Codex executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := NewSessions(executable, []string{"CODEX_HOME=" + t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(protocol.StreamEvent) error { return nil })
	defer p.Close()
	p.SetCollaborationTransport(func(context.Context, sessiontools.Ref, string, string, json.RawMessage) (sessiontools.Reply, error) {
		return sessiontools.Reply{Status: "succeeded"}, nil
	})
	a, release, err := p.acquire("discovery")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = a.Warm(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := a.request(ctx, "thread/start", map[string]any{"cwd": t.TempDir(), "approvalPolicy": "on-request", "sandbox": "read-only", "ephemeral": true})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = json.Unmarshal(raw, &started); err != nil || started.Thread.ID == "" {
		t.Fatalf("thread/start: %s %v", raw, err)
	}
	for {
		raw, err = a.request(ctx, "mcpServerStatus/list", map[string]any{"threadId": started.Thread.ID, "limit": 100, "detail": "toolsAndAuthOnly"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "wio_create_session") && strings.Contains(string(raw), "wio_wait_sessions") {
			t.Log("real Codex discovered collaboration tools without a model turn")
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("tools not discovered: %s", raw)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestCollaborationSourceEpochChangesAcrossTurns(t *testing.T) {
	p, _ := sessionTestPool(t)
	p.SetCollaborationTransport(func(context.Context, sessiontools.Ref, string, string, json.RawMessage) (sessiontools.Reply, error) {
		return sessiontools.Reply{}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: "epoch", OperationID: "turn-one", Workspace: t.TempDir(), Prompt: "work"}); err != nil {
		t.Fatal(err)
	}
	a := p.sessions["epoch"].adapter
	a.mu.Lock()
	epoch := a.toolOperationID
	configured := a.mcpStart != nil
	a.mu.Unlock()
	if epoch != "turn-one" || !configured {
		t.Fatalf("wrong binding: %q %v", epoch, configured)
	}
}
