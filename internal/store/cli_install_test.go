package store

import (
	"context"
	"errors"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestParallelCLIInstallGuards(t *testing.T) { assertParallelCLIInstalls(t, testStore(t)) }

func assertParallelCLIInstalls(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "parallel-cli", "parallel-cli-token")
	heartbeat := protocol.Heartbeat{AgentVersion: "0.3.60", Capabilities: []string{protocol.CapabilityCLIManage, protocol.CapabilityClaudeManage, protocol.CapabilityCLIInstallParallel}}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, kind := range []string{"codex", "gemini", "claude"} {
		id, err := db.QueueCLIInstall(ctx, server.ID, protocol.CLIInstallCommand{Kind: kind, Version: "1.0.0"})
		if err != nil {
			t.Fatalf("queue %s while other engines install: %v", kind, err)
		}
		ids[kind] = id
	}
	for kind, id := range ids {
		if _, err := db.QueueCLIInstall(ctx, server.ID, protocol.CLIInstallCommand{Kind: kind, Version: "2.0.0"}); !errors.Is(err, ErrCLIInstallBusy) {
			t.Fatalf("duplicate %s: %v", kind, err)
		}
		blockers, err := db.CLIInstallBlockers(ctx, server.ID, kind)
		if err != nil || len(blockers.BlockingOperations) != 1 || blockers.BlockingOperations[0].ID != id {
			t.Fatalf("%s blockers: %+v %v", kind, blockers, err)
		}
	}
	if _, err := db.QueueAgentRestart(ctx, server.ID, protocol.AgentAccessCommand{ServerID: server.ID}); !errors.Is(err, ErrAgentUpdateBusy) {
		t.Fatalf("restart during installation: %v", err)
	}
	// A missing declaration must retain server-wide exclusion, including on a
	// new-version Agent. Never infer this ability from its version number.
	heartbeat.Capabilities = []string{protocol.CapabilityCLIManage}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	blockers, err := db.CLIInstallBlockers(ctx, server.ID, "gemini")
	if err != nil || len(blockers.BlockingOperations) != 3 {
		t.Fatalf("legacy exclusion: %+v %v", blockers, err)
	}
}
