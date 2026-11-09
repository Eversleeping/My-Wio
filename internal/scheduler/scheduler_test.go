package scheduler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
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
	var promptMetadata struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(events[0].Payload, &promptMetadata); err != nil || promptMetadata.OperationID != updated.LastOperationID {
		t.Fatalf("scheduled prompt lacks operation association: %s (%v)", events[0].Payload, err)
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

func TestTickQueuesClaudeAndSkipsUnsafeStates(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	_, err = db.CreateEnrollment(ctx, "claude", []string{"/srv"}, "claude-token", time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	enrollment, _ := db.ConsumeEnrollment(ctx, "claude-token")
	server, err := db.EnrollServer(ctx, enrollment, "claude.local", "agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.93"}); err != nil {
		t.Fatal(err)
	}
	if err = db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude", Name: "claude"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "custom-model", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateScheduledTask(ctx, store.ScheduledTaskInput{AgentKind: "claude", ThreadID: thread.ID, Name: "review", Prompt: "review the code", Schedule: "@hourly", Timezone: "UTC", Enabled: true, ApprovalMode: "plan", ReasoningEffort: "max"}, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	gateway := agentgateway.New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner := &Runner{store: db, gateway: gateway, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	runner.tick(ctx)
	saved, err := db.ScheduledTask(ctx, task.ID)
	if err != nil || saved.LastRunStatus != "queued" || saved.LastOperationID == "" {
		t.Fatalf("not dispatched: %+v %v", saved, err)
	}
	op, _ := db.Operation(ctx, saved.LastOperationID)
	var c protocol.ClaudeTurnCommand
	if json.Unmarshal([]byte(op.Payload), &c) != nil || c.Model != "custom-model" || c.PermissionMode != "plan" || c.Effort != "max" {
		t.Fatalf("bad command: %+v", c)
	}
	for _, state := range []string{"busy", "archived", "offline", "old-agent"} {
		if state != "busy" {
			thread, _ = db.ClaudeThread(ctx, thread.ID)
			if thread.Status == "queued" {
				if err = db.InterruptClaudeTurn(ctx, thread); err != nil {
					t.Fatal(err)
				}
			}
		}
		if state == "archived" {
			if err = db.UpdateClaudeThread(ctx, thread.ID, "review", true); err != nil {
				t.Fatal(err)
			}
		}
		if state == "offline" {
			_ = db.UpdateClaudeThread(ctx, thread.ID, "review", false)
			_, err = db.DB.ExecContext(ctx, db.Q("UPDATE servers SET last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-time.Hour), server.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		if state == "old-agent" {
			_ = db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.92"})
		}
		if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE claude_scheduled_tasks SET next_run_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), task.ID); err != nil {
			t.Fatal(err)
		}
		runner.tick(ctx)
		saved, _ = db.ScheduledTask(ctx, task.ID)
		expected := "skipped"
		if state == "old-agent" {
			expected = "failed"
		}
		if saved.LastRunStatus != expected {
			t.Fatalf("%s: %+v", state, saved)
		}
	}
}

// schedulerFixture enrolls an online server with one workspace.
func schedulerFixture(t *testing.T) (*store.Store, store.Server, store.Workspace, *Runner) {
	t.Helper()
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
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "scheduler-node", AgentVersion: "0.2.97", CodexReady: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/scheduler", Name: "scheduler"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	gateway := agentgateway.New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return database, server, workspaces[0], &Runner{store: database, gateway: gateway, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func scheduledTask(t *testing.T, database *store.Store, input store.ScheduledTaskInput, nextRunAt time.Time) store.ScheduledTask {
	t.Helper()
	if input.Name == "" {
		input.Name, input.Prompt, input.Enabled = "review", "Review the repository", true
	}
	if input.Schedule == "" {
		input.Schedule, input.Timezone = "@hourly", "UTC"
	}
	task, err := database.CreateScheduledTask(context.Background(), input, nextRunAt)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestTickTreatsStaleHeartbeatAsOffline(t *testing.T) {
	database, server, workspace, runner := schedulerFixture(t)
	ctx := context.Background()
	thread, err := database.CreateThread(ctx, workspace.ID, "scheduled session")
	if err != nil {
		t.Fatal(err)
	}
	task := scheduledTask(t, database, store.ScheduledTaskInput{ThreadID: thread.ID}, time.Now().UTC().Add(-time.Minute))
	// The control plane crashed while the Agent was connected, so the status
	// column still says online although the Agent stopped sending heartbeats.
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE servers SET status='online',last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-time.Hour), server.ID); err != nil {
		t.Fatal(err)
	}
	runner.tick(ctx)
	skipped, err := database.ScheduledTask(ctx, task.ID)
	if err != nil || skipped.LastRunStatus != "skipped" || skipped.LastRunMessage != "server is offline" || skipped.LastOperationID != "" {
		t.Fatalf("turn queued for an offline server: %#v %v", skipped, err)
	}
	if operations, err := database.PendingOperations(ctx, server.ID); err != nil || len(operations) != 0 {
		t.Fatalf("operations queued for an offline server: %#v %v", operations, err)
	}
}

func TestTickDisablesTasksWithInvalidSchedules(t *testing.T) {
	database, _, workspace, runner := schedulerFixture(t)
	ctx := context.Background()
	thread, err := database.CreateThread(ctx, workspace.ID, "scheduled session")
	if err != nil {
		t.Fatal(err)
	}
	// More broken tasks than one tick loads, all due before the valid one.
	start := time.Now().UTC().Add(-2 * time.Hour)
	broken := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		input := store.ScheduledTaskInput{ThreadID: thread.ID, Schedule: "not a schedule", Timezone: "UTC"}
		if i%2 == 1 {
			input.Schedule, input.Timezone = "0 9 * * *", "Mars/Olympus"
		}
		broken = append(broken, scheduledTask(t, database, input, start.Add(time.Duration(i)*time.Second)).ID)
	}
	valid := scheduledTask(t, database, store.ScheduledTaskInput{ThreadID: thread.ID, ApprovalMode: "never"}, time.Now().UTC().Add(-time.Minute))

	runner.tick(ctx)
	runner.tick(ctx)

	for _, id := range broken {
		task, err := database.ScheduledTask(ctx, id)
		if err != nil || task.Enabled || task.LastRunStatus != "failed" || !strings.HasPrefix(task.LastRunMessage, "task disabled: invalid schedule") {
			t.Fatalf("broken task was not disabled: %#v %v", task, err)
		}
	}
	queued, err := database.ScheduledTask(ctx, valid.ID)
	if err != nil || queued.LastRunStatus != "queued" || !queued.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("valid task was starved: %#v %v", queued, err)
	}
	if due, err := database.DueScheduledTasks(ctx, time.Now().UTC(), 100); err != nil || len(due) != 0 {
		t.Fatalf("tasks still due: %d %v", len(due), err)
	}
}

func TestTickAttributesResultsThatArriveBeforeTheRunIsRecorded(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "gemini"} {
		t.Run(kind, func(t *testing.T) {
			database, _, workspace, runner := schedulerFixture(t)
			ctx := context.Background()
			var threadID, userMessage string
			switch kind {
			case "codex":
				thread, err := database.CreateThread(ctx, workspace.ID, "scheduled session")
				if err != nil {
					t.Fatal(err)
				}
				threadID, userMessage = thread.ID, "user.message"
			case "claude":
				thread, err := database.CreateClaudeThread(ctx, workspace.ID, "sonnet", "default", "")
				if err != nil {
					t.Fatal(err)
				}
				threadID, userMessage = thread.ID, "claude.user.message"
			case "gemini":
				thread, err := database.CreateGeminiThread(ctx, workspace.ID, "test-model", "default")
				if err != nil {
					t.Fatal(err)
				}
				threadID, userMessage = thread.ID, "gemini.user.message"
			}
			task := scheduledTask(t, database, store.ScheduledTaskInput{AgentKind: kind, ThreadID: threadID}, time.Now().UTC().Add(-time.Minute))
			// Play the gateway at the earliest point the Agent could answer:
			// once the prompt is recorded, the turn fails and the result is
			// attributed to whichever task references the operation.
			trigger := `CREATE TRIGGER agent_fails_fast AFTER INSERT ON events WHEN NEW.kind='` + userMessage + `' BEGIN
				UPDATE agent_operations SET status='failed',result='Agent is updating' WHERE status='queued' AND kind IN ('codex.turn.start','claude.turn.start','gemini.turn.start');
				UPDATE ` + kind + `_scheduled_tasks SET last_run_status='failed',last_run_message='Agent is updating' WHERE last_operation_id IN (SELECT id FROM agent_operations WHERE result='Agent is updating') AND last_run_status IN ('queued','running');
			END`
			if _, err := database.DB.ExecContext(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			runner.tick(ctx)
			current, err := database.ScheduledTask(ctx, task.ID)
			if err != nil || current.LastOperationID == "" || current.LastRunStatus != "failed" || current.LastRunMessage != "Agent is updating" {
				t.Fatalf("fast failure was overwritten: %#v %v", current, err)
			}
		})
	}
}

func TestTickSeparatesBusyWorkspacesFromQueueFailures(t *testing.T) {
	for _, kind := range []string{"claude", "gemini"} {
		t.Run(kind, func(t *testing.T) {
			database, _, workspace, runner := schedulerFixture(t)
			ctx := context.Background()
			var threadID, agent string
			if kind == "claude" {
				thread, err := database.CreateClaudeThread(ctx, workspace.ID, "sonnet", "default", "")
				if err != nil {
					t.Fatal(err)
				}
				threadID, agent = thread.ID, "Claude"
			} else {
				thread, err := database.CreateGeminiThread(ctx, workspace.ID, "test-model", "default")
				if err != nil {
					t.Fatal(err)
				}
				threadID, agent = thread.ID, "Gemini"
			}
			task := scheduledTask(t, database, store.ScheduledTaskInput{AgentKind: kind, ThreadID: threadID}, time.Now().UTC().Add(-time.Minute))

			// Another session's turn holds the workspace write lock.
			other, err := database.CreateGeminiThread(ctx, workspace.ID, "test-model", "default")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.QueueGeminiTurn(ctx, other, "occupy the workspace", "test-model", "default"); err != nil {
				t.Fatal(err)
			}
			runner.tick(ctx)
			skipped, err := database.ScheduledTask(ctx, task.ID)
			if err != nil || skipped.LastRunStatus != "skipped" || skipped.LastRunMessage != "workspace has another active write operation" {
				t.Fatalf("busy workspace: %#v %v", skipped, err)
			}

			other, _ = database.GeminiThread(ctx, other.ID)
			if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: other.OperationID, Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
			if _, err := database.DB.ExecContext(ctx, `CREATE TRIGGER database_fails BEFORE INSERT ON agent_operations WHEN NEW.kind='`+kind+`.turn.start' BEGIN SELECT RAISE(ABORT,'simulated database failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE "+kind+"_scheduled_tasks SET next_run_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), task.ID); err != nil {
				t.Fatal(err)
			}
			runner.tick(ctx)
			failed, err := database.ScheduledTask(ctx, task.ID)
			if err != nil || failed.LastRunStatus != "failed" || !strings.HasPrefix(failed.LastRunMessage, "could not queue "+agent+" turn: ") || !strings.Contains(failed.LastRunMessage, "simulated database failure") {
				t.Fatalf("database failure reported as: %#v %v", failed, err)
			}
		})
	}
}
