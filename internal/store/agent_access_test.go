package store

import (
	"context"
	"errors"
	"github.com/wio-platform/wio/internal/protocol"
	"sync"
	"testing"
)

func TestRuntimeAccessSerializesRestartAndBlocksMaintenance(t *testing.T) {
	assertRuntimeAccessSerializesRestartAndBlocksMaintenance(t, testStore(t))
}
func assertRuntimeAccessSerializesRestartAndBlocksMaintenance(t *testing.T, db *Store) {
	ctx := context.Background()
	server := enrollProjectImportTestServer(t, db, "runtime-access-lock")
	deploy, err := db.QueueOperation(ctx, server.ID, protocol.OpDeployStart, map[string]any{}, "runtime-access-deploy")
	if err != nil {
		t.Fatal(err)
	}
	command := protocol.AgentAccessCommand{ServerID: server.ID, Settings: &protocol.AgentAccessSettings{AllowSudo: true}}
	if _, err = db.QueueAgentAccessUpdate(ctx, server.ID, command); !errors.Is(err, ErrAgentUpdateBusy) {
		t.Fatalf("active deployment: %v", err)
	}
	if _, err = db.QueueAgentRestart(ctx, server.ID, protocol.AgentAccessCommand{ServerID: server.ID}); !errors.Is(err, ErrAgentUpdateBusy) {
		t.Fatalf("active deployment did not block explicit restart: %v", err)
	}
	if err = db.CompleteOperation(ctx, protocol.OperationResult{OperationID: deploy, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(restart bool) {
			defer wg.Done()
			var err error
			if restart {
				_, err = db.QueueAgentRestart(ctx, server.ID, protocol.AgentAccessCommand{ServerID: server.ID})
			} else {
				_, err = db.QueueAgentAccessUpdate(ctx, server.ID, command)
			}
			results <- err
		}(i%2 == 0)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrAgentUpdateBusy) {
			t.Fatalf("unexpected queue error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("queued %d simultaneous restarts", succeeded)
	}
	if _, err = db.QueueAgentRestart(ctx, server.ID, protocol.AgentAccessCommand{ServerID: server.ID}); !errors.Is(err, ErrAgentUpdateBusy) {
		t.Fatalf("pending maintenance did not block explicit restart: %v", err)
	}
	if _, err = db.QueueAgentUpdate(ctx, server.ID, protocol.AgentUpdateCommand{Version: "9.9.9"}); !errors.Is(err, ErrAgentUpdateBusy) {
		t.Fatalf("runtime update did not block Agent update: %v", err)
	}
	if _, err = db.QueueCLIInstall(ctx, server.ID, protocol.CLIInstallCommand{Kind: "codex", Version: "1.0.0"}); !errors.Is(err, ErrCLIInstallBusy) {
		t.Fatalf("runtime update did not block CLI installation: %v", err)
	}
}
