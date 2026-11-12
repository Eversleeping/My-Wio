package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type concurrentTestSession struct {
	db  *Store
	ref sessiontools.Ref
}

func newConcurrentTestSession(t *testing.T, db *Store, workspace, engine string) concurrentTestSession {
	t.Helper()
	ref := sessiontools.Ref{Engine: engine}
	var err error
	if engine == "claude" {
		var thread ClaudeThread
		thread, err = db.CreateClaudeThread(context.Background(), workspace, "sonnet", "default", "")
		ref.ID = thread.ID
	} else {
		var thread GeminiThread
		thread, err = db.CreateGeminiThread(context.Background(), workspace, "auto", "default")
		ref.ID = thread.ID
	}
	if err != nil {
		t.Fatal(err)
	}
	return concurrentTestSession{db, ref}
}

func (s concurrentTestSession) queue(ctx context.Context, prompt string) error {
	if s.ref.Engine == "claude" {
		thread, err := s.db.ClaudeThread(ctx, s.ref.ID)
		if err != nil {
			return err
		}
		_, err = s.db.QueueClaudeTurn(ctx, thread, prompt, "sonnet", "default", "")
		return err
	}
	thread, err := s.db.GeminiThread(ctx, s.ref.ID)
	if err != nil {
		return err
	}
	_, err = s.db.QueueGeminiTurn(ctx, thread, prompt, "auto", "default")
	return err
}

func (s concurrentTestSession) interrupt(ctx context.Context) error {
	if s.ref.Engine == "claude" {
		thread, err := s.db.ClaudeThread(ctx, s.ref.ID)
		if err != nil {
			return err
		}
		return s.db.InterruptClaudeTurn(ctx, thread)
	}
	thread, err := s.db.GeminiThread(ctx, s.ref.ID)
	if err != nil {
		return err
	}
	return s.db.InterruptGeminiTurn(ctx, thread)
}

func TestAgentConcurrentSessions(t *testing.T) { assertAgentConcurrentSessions(t, testStore(t)) }

func assertAgentConcurrentSessions(t *testing.T, db *Store) {
	for _, source := range []string{"codex", "claude", "gemini"} {
		for _, target := range []string{"claude", "gemini"} {
			t.Run(source+"-"+target, func(t *testing.T) {
				ctx := context.Background()
				f := newCollaborationFixture(t, db, source)
				second := newConcurrentTestSession(t, db, f.workspace, target)
				if err := second.queue(ctx, "parallel"); err != nil {
					t.Fatalf("another session blocked the turn: %v", err)
				}
				if err := second.queue(ctx, "duplicate"); !errors.Is(err, ErrThreadActive) {
					t.Fatalf("overlapping turns in one session: %v", err)
				}
				events, err := db.Events(ctx, second.ref.ID, 0, 100)
				if err != nil || len(events) != 1 {
					t.Fatalf("rejected turn changed history: %+v %v", events, err)
				}
				// Collaboration must follow the same sharing rules as direct turns.
				child := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "parallel", Engine: target, Prompt: "verify"})
				if child.Error != nil || child.Status != "queued" || child.OperationID == "" || child.Session == nil {
					t.Fatalf("child could not start during the parent turn: %+v", child)
				}
				wait := f.call(t, "wio_wait_sessions", sessiontools.Params{Sessions: []sessiontools.Ref{*child.Session}})
				if wait.Error != nil {
					t.Fatalf("parallel child incorrectly requires the parent to finish: %+v", wait)
				}
				empty := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "empty", Engine: target})
				if empty.Error != nil || empty.Session == nil {
					t.Fatalf("create empty child: %+v", empty)
				}
				sent := f.call(t, "wio_send_message", sessiontools.Params{RequestID: "send", Session: *empty.Session, Prompt: "run now", Busy: "queue"})
				if sent.Error != nil || sent.Status != "queued" || sent.OperationID == "" {
					t.Fatalf("queue preference unnecessarily deferred an independent turn: %+v", sent)
				}
				workspace, err := db.Workspace(ctx, f.workspace)
				if err != nil {
					t.Fatal(err)
				}
				workspace.ManagementMode = "managed"
				for _, kind := range []string{"workspace.file.write", "git.workspace.write"} {
					if _, err := db.QueueResourceOperation(ctx, f.server, kind, map[string]string{}, NewID(), OperationResource{WorkspaceID: f.workspace}, true); !errors.Is(err, ErrWorkspaceWriteActive) {
						t.Fatalf("%s bypassed active turns: %v", kind, err)
					}
				}
				if _, err := db.QueueWorkspaceLifecycle(ctx, workspace, protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, ProjectID: workspace.ProjectID, SourcePath: workspace.Path, Action: "delete"}); !errors.Is(err, ErrWorkspaceWriteActive) {
					t.Fatalf("workspace deletion bypassed active turns: %v", err)
				}
				// Releasing one session must leave the others' reservations intact.
				if err := second.interrupt(ctx); err != nil {
					t.Fatal(err)
				}
				if busy, err := db.HasActiveWorkspaceWriteOperation(ctx, f.workspace); err != nil || !busy {
					t.Fatalf("stopping one session released the whole workspace: %v %v", busy, err)
				}
				for _, operationID := range []string{f.operation, child.OperationID, sent.OperationID} {
					if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: operationID, Status: "succeeded"}); err != nil {
						t.Fatal(err)
					}
				}
				key := NewID()
				writer, err := db.QueueResourceOperation(ctx, f.server, "workspace.file.write", map[string]string{}, key, OperationResource{WorkspaceID: f.workspace}, true)
				if err != nil {
					t.Fatal(err)
				}
				if retry, err := db.QueueResourceOperation(ctx, f.server, "workspace.file.write", map[string]string{}, key, OperationResource{WorkspaceID: f.workspace}, true); err != nil || retry != writer {
					t.Fatalf("writer retry lost idempotency: %q %v", retry, err)
				}
				if err := second.queue(ctx, "while writing"); !errors.Is(err, ErrWorkspaceWriteActive) {
					t.Fatalf("turn bypassed an exclusive writer: %v", err)
				}
				if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: writer, Status: "succeeded"}); err != nil {
					t.Fatal(err)
				}
				if err := second.queue(ctx, "after writing"); err != nil {
					t.Fatalf("turn did not resume after the writer finished: %v", err)
				}
			})
		}
	}
}

func TestSessionWorkspaceWriteRace(t *testing.T) { assertSessionWorkspaceWriteRace(t, testStore(t)) }

func assertSessionWorkspaceWriteRace(t *testing.T, db *Store) {
	for _, firstEngine := range []string{"claude", "gemini"} {
		for _, secondEngine := range []string{"claude", "gemini", "writer"} {
			t.Run(firstEngine+"-"+secondEngine, func(t *testing.T) {
				ctx := context.Background()
				f := newCollaborationFixture(t, db, "codex")
				first := newConcurrentTestSession(t, db, f.workspace, firstEngine)
				var second concurrentTestSession
				if secondEngine != "writer" {
					second = newConcurrentTestSession(t, db, f.workspace, secondEngine)
				}
				start := make(chan struct{})
				results := make(chan error, 2)
				var workers sync.WaitGroup
				workers.Add(2)
				go func() {
					defer workers.Done()
					<-start
					results <- first.queue(ctx, "first")
				}()
				go func() {
					defer workers.Done()
					<-start
					if secondEngine == "writer" {
						_, err := db.QueueResourceOperation(ctx, f.server, "workspace.file.write", map[string]string{}, NewID(), OperationResource{WorkspaceID: f.workspace}, true)
						results <- err
					} else {
						results <- second.queue(ctx, "second")
					}
				}()
				close(start)
				workers.Wait()
				close(results)
				succeeded, conflicts := 0, 0
				for err := range results {
					if err == nil {
						succeeded++
					} else if errors.Is(err, ErrWorkspaceWriteActive) {
						conflicts++
					} else {
						t.Fatalf("unexpected queue error: %v", err)
					}
				}
				if secondEngine == "writer" && (succeeded != 1 || conflicts != 1) || secondEngine != "writer" && succeeded != 2 {
					t.Fatalf("succeeded=%d conflicts=%d", succeeded, conflicts)
				}
			})
		}
	}
}

func TestSessionConcurrencyMigratesLegacyIndex(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "legacy.db") + "?_pragma=foreign_keys(1)"
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	f := newCollaborationFixture(t, db, "claude")
	if _, err := db.DB.ExecContext(ctx, "CREATE UNIQUE INDEX operations_workspace_active_write_unique ON agent_operations(workspace_id) WHERE workspace_id IS NOT NULL AND workspace_write=1 AND status IN ('queued','delivered','running')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"claude", "gemini"} {
		db, err := Open(dsn)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer db.Close()
			thread := newConcurrentTestSession(t, db, f.workspace, engine)
			if err := thread.queue(ctx, "after migration"); err != nil {
				t.Fatalf("legacy index still blocks %s after reopen: %v", engine, err)
			}
		}()
	}
}
