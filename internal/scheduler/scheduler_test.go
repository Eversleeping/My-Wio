package scheduler

import (
	"context"
	"encoding/json"
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

func TestTickQueuesGeminiAndSkipsBusyArchivedOffline(t *testing.T) {
	for _, mode := range []string{"default", "auto_edit", "yolo"} {
		t.Run(mode, func(t *testing.T) {
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
			if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "scheduler-node", AgentVersion: "0.2.70", CodexReady: true}); err != nil {
				t.Fatal(err)
			}
			if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/scheduler", Name: "scheduler"}}}); err != nil {
				t.Fatal(err)
			}
			workspaces, err := database.ListWorkspaces(ctx)
			if err != nil || len(workspaces) != 1 {
				t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
			}
			thread, err := database.CreateGeminiThread(ctx, workspaces[0].ID, "test-model", "default", "medium")
			if err != nil {
				t.Fatal(err)
			}
			task, err := database.CreateScheduledTask(ctx, store.ScheduledTaskInput{
				AgentKind: "gemini", ThreadID: thread.ID, Name: "Hourly review", Prompt: "Review the repository", Schedule: "@hourly", Timezone: "UTC", Enabled: true, ApprovalMode: mode,
			}, time.Now().UTC().Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			gateway := agentgateway.New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if mode == "yolo" {
				runner := &Runner{store: database, gateway: gateway, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
				runner.tick(ctx)
				failed, err := database.ScheduledTask(ctx, task.ID)
				if err != nil || failed.LastRunStatus != "failed" || failed.LastOperationID != "" {
					t.Fatalf("old Agent accepted yolo: %+v %v", failed, err)
				}
				if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.72"}); err != nil {
					t.Fatal(err)
				}
				if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE gemini_scheduled_tasks SET next_run_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), task.ID); err != nil {
					t.Fatal(err)
				}
			}

			(&Runner{store: database, gateway: gateway, log: slog.New(slog.NewTextHandler(io.Discard, nil))}).tick(ctx)

			updated, err := database.ScheduledTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.LastRunStatus != "queued" || updated.LastOperationID == "" || !updated.NextRunAt.After(time.Now().UTC()) {
				t.Fatalf("scheduled task was not queued: %#v", updated)
			}
			thread, err = database.GeminiThread(ctx, thread.ID)
			if err != nil || thread.Status != "queued" {
				t.Fatalf("thread was not reserved: %#v %v", thread, err)
			}
			events, err := database.Events(ctx, thread.ID, 0, 10)
			if err != nil || len(events) != 1 || events[0].Kind != "gemini.user.message" {
				t.Fatalf("scheduled prompt event was not recorded: %#v %v", events, err)
			}
			operations, err := database.PendingOperations(ctx, server.ID)
			if err != nil || len(operations) != 1 || operations[0].Kind != "gemini.turn.start" {
				t.Fatalf("scheduled turn was not queued: %#v %v", operations, err)
			}

			var command protocol.GeminiTurnCommand
			if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.ThinkingLevel != "medium" || command.ApprovalMode != mode {
				t.Fatal("scheduled task lost thinking level or approval mode", err)
			}
			for _, state := range []string{"busy", "archived", "offline"} {
				if state == "archived" {
					database.CompleteGeminiTurn(ctx, thread.ID, thread.OperationID, "idle")
					database.UpdateGeminiThread(ctx, thread.ID, "archived", true)
				}
				if state == "offline" {
					database.UpdateGeminiThread(ctx, thread.ID, "unarchived", false)
					database.DB.ExecContext(ctx, database.Q("UPDATE servers SET last_seen_at=? WHERE id=?"), time.Now().Add(-time.Hour), server.ID)
				}
				database.DB.ExecContext(ctx, database.Q("UPDATE gemini_scheduled_tasks SET next_run_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), task.ID)
				(&Runner{store: database, gateway: gateway, log: slog.New(slog.NewTextHandler(io.Discard, nil))}).tick(ctx)
				skipped, err := database.ScheduledTask(ctx, task.ID)
				if err != nil || skipped.LastRunStatus != "skipped" {
					t.Fatalf("%s was not skipped: %+v %v", state, skipped, err)
				}
			}
		})
	}
}
