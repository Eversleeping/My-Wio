package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// scheduledTaskFixture enrolls a server with a fresh heartbeat and returns its
// only workspace.
func scheduledTaskFixture(t *testing.T, database *Store, name string) (Server, Workspace) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, database, name, name+"-token")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: name, AgentVersion: "0.2.97"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/" + name, Name: name}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, workspace := range workspaces {
		if workspace.ServerID == server.ID {
			return server, workspace
		}
	}
	t.Fatal("workspace missing")
	return Server{}, Workspace{}
}

func createTestScheduledTask(t *testing.T, database *Store, kind, threadID string, nextRunAt time.Time) ScheduledTask {
	t.Helper()
	task, err := database.CreateScheduledTask(context.Background(), ScheduledTaskInput{AgentKind: kind, ThreadID: threadID, Name: kind + " review", Prompt: "Review the repository", Schedule: "@hourly", Timezone: "UTC", Enabled: true}, nextRunAt)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestScheduledTaskServerStatusFollowsHeartbeat(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server, workspace := scheduledTaskFixture(t, database, "heartbeat")
	codex, err := database.CreateThread(ctx, workspace.ID, "scheduled")
	if err != nil {
		t.Fatal(err)
	}
	gemini, err := database.CreateGeminiThread(ctx, workspace.ID, "test-model", "default")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := database.CreateClaudeThread(ctx, workspace.ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Minute)
	ids := []string{
		createTestScheduledTask(t, database, "codex", codex.ID, due).ID,
		createTestScheduledTask(t, database, "gemini", gemini.ID, due).ID,
		createTestScheduledTask(t, database, "claude", claude.ID, due).ID,
	}
	assertStatus := func(want string) {
		t.Helper()
		for _, id := range ids {
			task, err := database.ScheduledTask(ctx, id)
			if err != nil || task.ServerStatus != want {
				t.Fatalf("%s task server status = %q, want %q (%v)", task.AgentKind, task.ServerStatus, want, err)
			}
		}
		listed, err := database.ListScheduledTasks(ctx)
		if err != nil || len(listed) != len(ids) {
			t.Fatalf("listed tasks: %#v %v", listed, err)
		}
		dueTasks, err := database.DueScheduledTasks(ctx, time.Now().UTC(), 10)
		if err != nil || len(dueTasks) != len(ids) {
			t.Fatalf("due tasks: %#v %v", dueTasks, err)
		}
		for _, task := range append(listed, dueTasks...) {
			if task.ServerStatus != want {
				t.Fatalf("%s task server status = %q, want %q", task.AgentKind, task.ServerStatus, want)
			}
		}
	}
	assertStatus("online")

	// After a control-plane crash the disconnect handler never resets the
	// column, so only the heartbeat age tells that the Agent is gone.
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE servers SET status='online',last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-2*ServerOnlineGracePeriod), server.ID); err != nil {
		t.Fatal(err)
	}
	assertStatus("offline")

	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE servers SET status='offline',last_seen_at=? WHERE id=?"), time.Now().UTC(), server.ID); err != nil {
		t.Fatal(err)
	}
	assertStatus("online")
}

func TestDisableScheduledTaskTurnsOffOnlyStillDueTasks(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	_, workspace := scheduledTaskFixture(t, database, "disable")
	thread, err := database.CreateThread(ctx, workspace.ID, "scheduled")
	if err != nil {
		t.Fatal(err)
	}
	task := createTestScheduledTask(t, database, "codex", thread.ID, time.Now().UTC().Add(-time.Minute))
	read, err := database.ScheduledTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The schedule was fixed after the scheduler read the broken task.
	fixedNext := time.Now().UTC().Add(time.Hour)
	if _, err := database.UpdateScheduledTask(ctx, task.ID, ScheduledTaskInput{AgentKind: "codex", ThreadID: thread.ID, Name: task.Name, Prompt: task.Prompt, Schedule: "@hourly", Timezone: "UTC", Enabled: true}, fixedNext); err != nil {
		t.Fatal(err)
	}
	disabled, err := database.DisableScheduledTask(ctx, task.ID, read.NextRunAt, "task disabled: invalid schedule", time.Now().UTC())
	if err != nil || disabled {
		t.Fatalf("a fixed task was disabled: %v %v", disabled, err)
	}
	if current, _ := database.ScheduledTask(ctx, task.ID); !current.Enabled {
		t.Fatalf("fixed task lost its enabled flag: %#v", current)
	}

	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE codex_scheduled_tasks SET timezone='Mars/Olympus',next_run_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), task.ID); err != nil {
		t.Fatal(err)
	}
	read, _ = database.ScheduledTask(ctx, task.ID)
	disabled, err = database.DisableScheduledTask(ctx, task.ID, read.NextRunAt, "task disabled: invalid schedule", time.Now().UTC())
	if err != nil || !disabled {
		t.Fatalf("due broken task was not disabled: %v %v", disabled, err)
	}
	current, err := database.ScheduledTask(ctx, task.ID)
	if err != nil || current.Enabled || current.LastRunStatus != "failed" || current.LastRunMessage != "task disabled: invalid schedule" {
		t.Fatalf("disabled task: %#v %v", current, err)
	}
	if due, err := database.DueScheduledTasks(ctx, time.Now().UTC(), 100); err != nil || len(due) != 0 {
		t.Fatalf("disabled task is still due: %#v %v", due, err)
	}
}

func TestQueueScheduledTurnsRecordTheRunInTheQueueTransaction(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	_, workspace := scheduledTaskFixture(t, database, "scheduled-turns")
	claude, err := database.CreateClaudeThread(ctx, workspace.ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	gemini, err := database.CreateGeminiThread(ctx, workspace.ID, "test-model", "default")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claudeTask := createTestScheduledTask(t, database, "claude", claude.ID, now)
	geminiTask := createTestScheduledTask(t, database, "gemini", gemini.ID, now)

	// A task deleted after it was claimed must not start a turn.
	if _, err := database.QueueScheduledClaudeTurn(ctx, "deleted-task", claude, "review", "sonnet", "default", "", now); err == nil {
		t.Fatal("turn queued for a missing task")
	}
	if thread, _ := database.ClaudeThread(ctx, claude.ID); thread.Status == "queued" || thread.OperationID != "" {
		t.Fatalf("failed scheduled queue left the session claimed: %#v", thread)
	}

	operationID, err := database.QueueScheduledClaudeTurn(ctx, claudeTask.ID, claude, "review", "sonnet", "default", "", now)
	if err != nil || operationID == "" {
		t.Fatalf("queue scheduled Claude turn: %q %v", operationID, err)
	}
	task, err := database.ScheduledTask(ctx, claudeTask.ID)
	if err != nil || task.LastRunStatus != "queued" || task.LastOperationID != operationID {
		t.Fatalf("run was not recorded with the turn: %#v %v", task, err)
	}
	if thread, _ := database.ClaudeThread(ctx, claude.ID); thread.OperationID != operationID {
		t.Fatalf("scheduled run does not reference the session's operation: %#v", thread)
	}

	// The workspace write lock is reported as busy, not as a database error,
	// and the losing task records nothing.
	if _, err := database.QueueScheduledGeminiTurn(ctx, geminiTask.ID, gemini, "review", "test-model", "default", "", now); !errors.Is(err, ErrWorkspaceWriteActive) {
		t.Fatalf("busy workspace error = %v", err)
	}
	if task, _ := database.ScheduledTask(ctx, geminiTask.ID); task.LastRunStatus != "" || task.LastOperationID != "" {
		t.Fatalf("rejected turn recorded a run: %#v", task)
	}

	// The Agent can answer as soon as the queue transaction commits; the
	// result must be attributed to the task.
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operationID, Status: "failed", Message: "Agent is updating"}); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkScheduledTaskByOperation(ctx, operationID, "failed", "Agent is updating"); err != nil {
		t.Fatal(err)
	}
	if task, _ := database.ScheduledTask(ctx, claudeTask.ID); task.LastRunStatus != "failed" || task.LastRunMessage != "Agent is updating" {
		t.Fatalf("fast result was not attributed to the task: %#v", task)
	}

	operationID, err = database.QueueScheduledGeminiTurn(ctx, geminiTask.ID, gemini, "review", "test-model", "default", "", now)
	if err != nil || operationID == "" {
		t.Fatalf("queue scheduled Gemini turn: %q %v", operationID, err)
	}
	if task, _ := database.ScheduledTask(ctx, geminiTask.ID); task.LastRunStatus != "queued" || task.LastOperationID != operationID {
		t.Fatalf("Gemini run was not recorded with the turn: %#v", task)
	}
	if thread, _ := database.GeminiThread(ctx, gemini.ID); thread.OperationID != operationID {
		t.Fatalf("scheduled run does not reference the session's operation: %#v", thread)
	}
}

func TestMarkScheduledCodexRunQueuedKeepsResultsThatArrivedFirst(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server, workspace := scheduledTaskFixture(t, database, "codex-run")
	thread, err := database.CreateThread(ctx, workspace.ID, "scheduled")
	if err != nil {
		t.Fatal(err)
	}
	task := createTestScheduledTask(t, database, "codex", thread.ID, time.Now().UTC())
	for _, test := range []struct {
		result      *protocol.OperationResult
		wantStatus  string
		wantMessage string
	}{
		{nil, "queued", ""},
		{&protocol.OperationResult{Status: "failed", Message: "Agent is updating"}, "failed", "Agent is updating"},
		{&protocol.OperationResult{Status: "succeeded"}, "running", ""},
	} {
		operationID, err := database.QueueResourceOperation(ctx, server.ID, "codex.turn.start", map[string]string{"thread_id": thread.ID}, NewID(), OperationResource{ThreadID: thread.ID}, false)
		if err != nil {
			t.Fatal(err)
		}
		if test.result != nil {
			// The result arrives before the run is recorded, so the gateway
			// cannot attribute it to the task yet.
			test.result.OperationID = operationID
			if err := database.CompleteOperation(ctx, *test.result); err != nil {
				t.Fatal(err)
			}
			if err := database.MarkScheduledTaskByOperation(ctx, operationID, test.result.Status, test.result.Message); err != nil {
				t.Fatal(err)
			}
		}
		if err := database.MarkScheduledCodexRunQueued(ctx, task.ID, operationID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		current, err := database.ScheduledTask(ctx, task.ID)
		if err != nil || current.LastOperationID != operationID || current.LastRunStatus != test.wantStatus || current.LastRunMessage != test.wantMessage {
			t.Fatalf("run = %q %q (%q), want %q %q: %v", current.LastRunStatus, current.LastRunMessage, current.LastOperationID, test.wantStatus, test.wantMessage, err)
		}
		if test.result == nil {
			if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operationID, Status: "cancelled"}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
