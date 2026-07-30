package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestTickQueuesDueTaskAndRecordsPrompt(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "scheduler-node", []string{"/srv"}, "scheduler-enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "scheduler-enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "scheduler-node.local", "scheduler-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "scheduler-node", AgentVersion: "0.1.0", CodexReady: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/scheduler", Name: "scheduler"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "scheduled session")
	if err != nil {
		t.Fatal(err)
	}
	task, err := database.CreateScheduledTask(ctx, store.ScheduledTaskInput{
		ThreadID: thread.ID, Name: "Hourly review", Prompt: "Review the repository", Schedule: "@hourly", Timezone: "UTC", Enabled: true, ApprovalMode: "never",
	}, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	gateway := agentgateway.New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	(&Runner{store: database, gateway: gateway, log: slog.New(slog.NewTextHandler(io.Discard, nil))}).tick(ctx)

	updated, err := database.ScheduledTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastRunStatus != "queued" || updated.LastOperationID == "" || !updated.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("scheduled task was not queued: %#v", updated)
	}
	thread, err = database.Thread(ctx, thread.ID)
	if err != nil || thread.Status != "queued" {
		t.Fatalf("thread was not reserved: %#v %v", thread, err)
	}
	events, err := database.Events(ctx, thread.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "user.message" {
		t.Fatalf("scheduled prompt event was not recorded: %#v %v", events, err)
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "codex.turn.start" {
		t.Fatalf("scheduled turn was not queued: %#v %v", operations, err)
	}
}
