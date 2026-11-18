package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessiontools"
	"github.com/wio-platform/wio/internal/task"
)

func TestTaskExecution(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "execution.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertTaskExecution(t, db)
}

func newTaskExecution(t *testing.T, db *Store) (taskFixture, task.Task, task.Plan, task.Run) {
	t.Helper()
	return newTaskExecutionPlan(t, db, nil)
}
func newTaskExecutionPlan(t *testing.T, db *Store, edit func(*task.AppendPlan)) (taskFixture, task.Task, task.Plan, task.Run) {
	t.Helper()
	ctx := context.Background()
	f := newTaskFixture(t, db)
	created := f.create(t, "create-execution")
	if _, err := db.DB.Exec(db.Q("UPDATE servers SET agent_version=?,last_seen_at=? WHERE id=?"), buildinfo.Version, time.Now().UTC(), f.server); err != nil {
		t.Fatal(err)
	}
	declared, _ := json.Marshal(capabilities.AgentDeclaration("0.68.0", "2.1.0"))
	if _, err := db.DB.Exec(db.Q("INSERT INTO server_capabilities(server_id,capabilities) VALUES(?,?)"), f.server, string(declared)); err != nil {
		t.Fatal(err)
	}
	p := f.plan(1, "plan-execution")
	for i, engine := range []string{"codex", "gemini", "claude"} {
		profile := NewID()
		if _, err := db.DB.Exec(db.Q("INSERT INTO credential_profiles(id,kind,name,endpoint,ciphertext) VALUES(?,?,?,'','fixture')"), profile, engine, profile); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(db.Q("INSERT INTO server_engines(server_id,engine,cli_version,ready,credential_profile_id) VALUES(?,?,'1.0',1,?)"), f.server, engine, profile); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			workspace := NewID()
			if _, err := db.DB.Exec(db.Q("INSERT INTO workspaces(id,project_id,server_id,path,status) VALUES(?,?,?,?,'ready')"), workspace, f.project, f.server, "/srv/"+workspace); err != nil {
				t.Fatal(err)
			}
			p.Members[i].WorkspaceID = workspace
		}
	}
	p.Steps = []task.Step{
		{ID: "a", MemberID: "dev", Kind: "plan", Title: "Plan", Instructions: "Make a plan", Required: true, CompletionPolicy: "execution"},
		{ID: "b", MemberID: "research", Kind: "research", Title: "Research", Instructions: "Research", Required: true, CompletionPolicy: "execution", DependsOn: []string{"a"}},
		{ID: "c", MemberID: "review", Kind: "develop", Title: "Develop", Instructions: "Implement", Required: true, CompletionPolicy: "execution", DependsOn: []string{"a"}},
		{ID: "d", MemberID: "dev", Kind: "summarize", Title: "Summary", Instructions: "Summarize", Required: true, CompletionPolicy: "execution", DependsOn: []string{"b", "c"}, Inputs: []task.Input{{Kind: "step_result", StepID: "b"}, {Kind: "step_result", StepID: "c"}}},
	}
	if edit != nil {
		edit(&p)
	}
	plan, err := db.AppendTaskPlan(ctx, f.project, created.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	in := task.GrantInput{Meta: task.Meta{Actor: "task-user", RequestID: "grant-execution", ExpectedVersion: 2}, Enabled: true, Budgets: task.DefaultBudgets(), ExecutionModes: map[string]task.ExecutionMode{}}
	for _, m := range p.Members {
		in.AllowedMembers = append(in.AllowedMembers, m.ID)
		if !taskContains(in.WorkspaceIDs, m.WorkspaceID) {
			in.WorkspaceIDs = append(in.WorkspaceIDs, m.WorkspaceID)
		}
		in.ExecutionModes[m.ID] = task.ExecutionMode{Engine: m.Engine, Mode: m.Mode}
	}
	g, err := db.SetTaskGrant(ctx, f.project, created.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	start := task.Start{Meta: task.Meta{Actor: "task-user", RequestID: "start-execution", ExpectedVersion: g.TaskVersion}, PlanID: plan.Plan.ID, GrantRevision: g.Grant.Revision}
	r, err := db.StartTaskRun(ctx, f.project, created.ID, start)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := db.StartTaskRun(ctx, f.project, created.ID, start)
	if err != nil || replay.Run.ID != r.Run.ID {
		t.Fatal("start did not replay", err)
	}
	return f, created, plan.Plan, r.Run
}
func taskAttemptFor(t *testing.T, db *Store, run, step string) task.Attempt {
	t.Helper()
	var a task.Attempt
	if err := db.DB.Get(&a, db.Q(taskAttemptSelect+" WHERE run_id=? AND step_id=? ORDER BY number DESC LIMIT 1"), run, step); err != nil {
		t.Fatal(err)
	}
	return a
}
func taskFinish(t *testing.T, db *Store, a task.Attempt, status string) {
	t.Helper()
	ctx := context.Background()
	r := protocol.OperationResult{OperationID: *a.OperationID, Status: status, Message: "verified native turn ended"}
	if err := db.RecordTaskOperationResult(ctx, a.ServerID, r, false); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteOperation(ctx, r); err != nil {
		t.Fatal(err)
	}
	if a.Engine == "codex" {
		nativeStatus := "completed"
		if status == "failed" {
			nativeStatus = "failed"
		}
		if status == "cancelled" {
			nativeStatus = "interrupted"
		}
		for _, e := range []protocol.StreamEvent{{StreamID: a.SessionID, Kind: protocol.EventTurnAccepted, Payload: json.RawMessage(`{"turn_id":"native-` + a.ID + `"}`)}, {StreamID: a.SessionID, Kind: protocol.EventCodexTurnCompleted, Payload: json.RawMessage(`{"turn":{"id":"native-` + a.ID + `","status":"` + nativeStatus + `"}}`)}} {
			if _, err := db.AddEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.SetThreadStatus(ctx, a.SessionID, "idle"); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := db.DB.Exec(db.Q("UPDATE "+a.Engine+"_threads SET status='idle' WHERE id=?"), a.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	f, ok, err := db.ClaimTaskAttempt(ctx, a.ID, "worker")
	if err != nil || !ok {
		t.Fatal("claim", err)
	}
	if err := db.ReconcileTaskAttempt(ctx, f); err != nil {
		t.Fatal(err)
	}
}
func assertTaskExecution(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	f, created, _, run := newTaskExecution(t, db)
	if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "d", "worker"); err != nil || op != "" {
		t.Fatal("join ran before dependencies", op, err)
	}
	// Two independent dispatchers race the same logical step at a barrier.
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	a := taskAttemptFor(t, db, run.ID, "a")
	var n int
	if err := db.DB.Get(&n, db.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=? AND step_id='a'"), run.ID); err != nil || n != 1 {
		t.Fatal("duplicate dispatch", n, err)
	}
	// A start receipt plus idle state cannot satisfy Codex completion.
	if err := db.RecordTaskOperationResult(ctx, a.ServerID, protocol.OperationResult{OperationID: *a.OperationID, Status: "succeeded"}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: *a.OperationID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetThreadStatus(ctx, a.SessionID, "idle"); err != nil {
		t.Fatal(err)
	}
	fence, ok, err := db.ClaimTaskAttempt(ctx, a.ID, "worker")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := db.ReconcileTaskAttempt(ctx, fence); err != nil {
		t.Fatal(err)
	}
	if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "b", "worker"); err != nil || op != "" {
		t.Fatal("Codex acceptance released dependency", op, err)
	}
	thread, err := db.Thread(ctx, a.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.QueueCodexTurn(ctx, thread, protocol.StartTurnCommand{ThreadID: thread.ID, Prompt: "manual"}); !errors.Is(err, ErrStateConflict) {
		t.Fatal("manual turn bypassed task ownership", err)
	}
	if err := db.taskMutationGuard(ctx, db.DB, session.Codex, a.SessionID); !errors.Is(err, ErrStateConflict) {
		t.Fatal("task guard", err)
	}
	// Expired lease transfers observation only, preserving the operation.
	if _, err := db.DB.Exec(db.Q("UPDATE task_attempts SET lease_expires_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), a.ID); err != nil {
		t.Fatal(err)
	}
	newFence, ok, err := db.ClaimTaskAttempt(ctx, a.ID, "replacement")
	if err != nil || !ok || newFence.Generation <= fence.Generation {
		t.Fatal("lease did not transfer", err)
	}
	if err := db.ReconcileTaskAttempt(ctx, fence); err == nil {
		t.Fatal("old lease wrote the projection")
	}
	if _, err := db.DB.Exec(db.Q("UPDATE task_attempts SET lease_expires_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), a.ID); err != nil {
		t.Fatal(err)
	}
	taskFinish(t, db, a, "succeeded")
	for _, step := range []string{"b", "c"} {
		if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, step, "worker"); err != nil || op == "" {
			t.Fatal("parallel branch not dispatched", step, err)
		}
	}
	b, c := taskAttemptFor(t, db, run.ID, "b"), taskAttemptFor(t, db, run.ID, "c")
	if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "d", "worker"); err != nil || op != "" {
		t.Fatal("join ran early", err)
	}
	taskFinish(t, db, b, "succeeded")
	if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "d", "worker"); err != nil || op != "" {
		t.Fatal("AND join released early", err)
	}
	taskFinish(t, db, c, "succeeded")
	if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "d", "worker"); err != nil || op == "" {
		t.Fatal("join did not dispatch", err)
	}
	d := taskAttemptFor(t, db, run.ID, "d")
	detailBefore, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ActOnTaskRun(ctx, f.project, created.ID, run.ID, task.Action{Meta: task.Meta{Actor: "task-user", RequestID: "takeover-old-active-session", ExpectedVersion: detailBefore.Run.Version}, Action: "takeover", AttemptID: a.ID, Reason: "take over old attempt"})
	taskCode(t, err, task.StateConflict)
	if d.SessionID != a.SessionID {
		t.Fatal("member did not reuse its run-specific session")
	}
	taskFinish(t, db, d, "succeeded")
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	detail, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || detail.Task.State != "awaiting_acceptance" {
		t.Fatal("execution not projected to awaiting acceptance", detail.Task.State, err)
	}
	// A late receipt for an old step cannot alter another step or acceptance.
	if err := db.RecordTaskOperationResult(ctx, a.ServerID, protocol.OperationResult{OperationID: *a.OperationID, Status: "failed"}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	assertTaskAcceptance(t, db, f, created, run)
	assertTaskUnknownCancel(t, db)
	assertTaskDispatchRollback(t, db)
	assertTaskPauseCancel(t, db)
	assertTaskTakeoverAndRevoke(t, db)
	assertTaskResourceBoundaries(t, db)
	assertTaskActionDispatchRace(t, db)
	assertTaskExecutionCommandRollback(t, db)
	assertTaskDeadlineInterrupt(t, db)
}

func taskRunAction(t *testing.T, db *Store, f taskFixture, id, run string, in task.Action) task.RunResult {
	t.Helper()
	d, err := db.TaskDetail(context.Background(), f.project, id)
	if err != nil {
		t.Fatal(err)
	}
	in.Meta = task.Meta{Actor: "task-user", RequestID: NewID(), ExpectedVersion: d.Run.Version}
	if in.Reason == "" {
		in.Reason = "explicit user action"
	}
	out, err := db.ActOnTaskRun(context.Background(), f.project, id, run, in)
	if err != nil {
		t.Fatal(in.Action, err)
	}
	return out
}

func assertTaskPauseCancel(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	f, created, _, run := newTaskExecution(t, db)
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	a := taskAttemptFor(t, db, run.ID, "a")
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "pause"})
	taskFinish(t, db, a, "succeeded")
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.DB.Get(&count, db.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=?"), run.ID); err != nil || count != 1 {
		t.Fatal("pause dispatched new work", err)
	}
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "resume"})
	for _, step := range []string{"b", "c"} {
		if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, step, "worker"); err != nil {
			t.Fatal(err)
		}
	}
	b, c := taskAttemptFor(t, db, run.ID, "b"), taskAttemptFor(t, db, run.ID, "c")
	if _, err := db.CancelOperation(ctx, *b.OperationID, "manual cancel"); !errors.Is(err, ErrStateConflict) {
		t.Fatal("native cancellation bypassed run ownership", err)
	}
	for _, attempt := range []task.Attempt{b, c} {
		if err := db.MarkDelivered(ctx, *attempt.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "cancel"})
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []task.Attempt{b, c} {
		a := taskAttemptFor(t, db, run.ID, attempt.StepID)
		if a.InterruptOperationID == nil {
			t.Fatal("missing durable interrupt", attempt.Engine)
		}
		op, err := db.Operation(ctx, *attempt.OperationID)
		if err != nil || op.Status != "delivered" {
			t.Fatal("delivered work was mistaken for unsent", op.Status, err)
		}
	}
	taskFinish(t, db, b, "cancelled")
	taskFinish(t, db, c, "cancelled")
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	d, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || d.Run.State != "cancelled" {
		t.Fatal("confirmed cancellation did not finish", err)
	}
	if err := db.DB.Get(&count, db.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=? AND step_id='d'"), run.ID); err != nil || count != 0 {
		t.Fatal("cancellation dispatched the join", err)
	}
}

func assertTaskTakeoverAndRevoke(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	f, created, _, run := newTaskExecution(t, db)
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	a := taskAttemptFor(t, db, run.ID, "a")
	taskFinish(t, db, a, "failed")
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "takeover", AttemptID: a.ID})
	if err := db.taskMutationGuard(ctx, db.DB, a.Engine, a.SessionID); err != nil {
		t.Fatal("takeover did not release the confirmed stopped session", err)
	}
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "retry_step", StepID: "a"})
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "resume"})
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	newAttempt := taskAttemptFor(t, db, run.ID, "a")
	if newAttempt.SessionID == a.SessionID || newAttempt.Number != 2 {
		t.Fatal("retry reused a session after manual takeover")
	}
	// An old operation's late events cannot satisfy the replacement attempt.
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: a.SessionID, Kind: protocol.EventCodexTurnCompleted, Payload: json.RawMessage(`{"turn":{"id":"old","status":"completed"}}`)}); err != nil {
		t.Fatal(err)
	}
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if latest := taskAttemptFor(t, db, run.ID, "a"); latest.State == "succeeded" {
		t.Fatal("late old evidence completed a replacement attempt")
	}
	taskFinish(t, db, newAttempt, "succeeded")
	d, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Grant
	in := task.GrantInput{Meta: task.Meta{Actor: "task-user", RequestID: NewID(), ExpectedVersion: d.Task.Version}, Enabled: true, AllowedMembers: []string{"dev"}, WorkspaceIDs: g.WorkspaceIDs, ExecutionModes: g.ExecutionModes, Budgets: g.Budgets}
	if _, err := db.SetTaskGrant(ctx, f.project, created.ID, in); err != nil {
		t.Fatal("could not narrow authorization", err)
	}
	if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "b", "worker"); err != nil || op != "" {
		t.Fatal("narrowed authorization dispatched work", err)
	}
	d, err = db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = d.Task.Version
	in.RequestID = NewID()
	in.Enabled = false
	if _, err := db.SetTaskGrant(ctx, f.project, created.ID, in); err != nil {
		t.Fatal(err)
	}
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	d, err = db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || d.Run.State != "cancelled" {
		t.Fatal("revoked authorization did not stop the run", err)
	}
}
func assertTaskUnknownCancel(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	f, created, _, run := newTaskExecution(t, db)
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	a := taskAttemptFor(t, db, run.ID, "a")
	if err := db.RecordTaskOperationResult(ctx, a.ServerID, protocol.OperationResult{OperationID: *a.OperationID, Status: "failed", Message: protocol.OperationLostMessage}, true); err != nil {
		t.Fatal(err)
	}
	fence, _, err := db.ClaimTaskAttempt(ctx, a.ID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReconcileTaskAttempt(ctx, fence); err != nil {
		t.Fatal(err)
	}
	a = taskAttemptFor(t, db, run.ID, "a")
	if a.State != "unknown" {
		t.Fatal("lost operation became a safe failure", a.State)
	}
	detail, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	action := task.Action{Meta: task.Meta{Actor: "task-user", RequestID: "cancel-execution", ExpectedVersion: detail.Run.Version}, Action: "cancel", Reason: "stop execution"}
	if _, err := db.ActOnTaskRun(ctx, f.project, created.ID, run.ID, action); err != nil {
		t.Fatal(err)
	}
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	detail, err = db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || detail.Run.State != "cancelling" {
		t.Fatal("unknown execution falsely cancelled", err)
	}
	action = task.Action{Meta: task.Meta{Actor: "task-user", RequestID: "retry-unknown", ExpectedVersion: detail.Run.Version}, Action: "retry_step", StepID: "a", Reason: "retry"}
	if _, err := db.ActOnTaskRun(ctx, f.project, created.ID, run.ID, action); err == nil {
		t.Fatal("unknown execution retried")
	}
	stopped := time.Now().UTC().Add(-time.Second)
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "resolve_unknown", AttemptID: a.ID, Resolution: "cancelled", StoppedAt: &stopped, Evidence: "Verified remote process stopped through independent inspection"})
	thread, err := db.Thread(ctx, a.SessionID)
	if err != nil || thread.Status != "interrupted" {
		t.Fatalf("confirmed stop left the native session busy: %+v %v", thread, err)
	}
	if err := db.ReconcileTaskAttempt(ctx, fence); err == nil {
		t.Fatal("resolved unknown accepted old lease")
	}
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	detail, err = db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || detail.Run.State != "cancelled" {
		t.Fatal("user-confirmed cancellation did not finish", err)
	}
}
func assertTaskDispatchRollback(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	_, created, _, run := newTaskExecution(t, db)
	for _, table := range []string{"codex_threads", "agent_operations", "task_session_bindings", "task_attempts", "events", "task_events"} {
		before := map[string]int{}
		for _, name := range []string{"codex_threads", "agent_operations", "task_session_bindings", "task_attempts", "events"} {
			before[name] = taskCount(t, db, name)
		}
		remove := injectTaskFailure(t, db, table)
		_, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker")
		remove()
		if err == nil {
			t.Fatal("no injected failure at", table)
		}
		for name, count := range before {
			if got := taskCount(t, db, name); got != count {
				t.Fatal(fmt.Sprintf("partial dispatch at %s: %s %d/%d", table, name, got, count))
			}
		}
	}
	if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil || op == "" {
		t.Fatal("same dispatch could not recover after rollback", err)
	}
}

func assertTaskResourceBoundaries(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	for _, sameMember := range []bool{false, true} {
		f, created, _, run := newTaskExecutionPlan(t, db, func(p *task.AppendPlan) {
			if sameMember {
				p.Steps[2].MemberID = p.Steps[1].MemberID
			} else {
				p.Members[2].WorkspaceID = p.Members[1].WorkspaceID
			}
		})
		if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
			t.Fatal(err)
		}
		taskFinish(t, db, taskAttemptFor(t, db, run.ID, "a"), "succeeded")
		if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "b", "worker"); err != nil || op == "" {
			t.Fatal("first branch", err)
		}
		if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "c", "worker"); err != nil || op != "" {
			t.Fatal("conflicting branch ran", sameMember, err)
		}
		d, err := db.TaskDetail(ctx, f.project, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		blocked := false
		for _, s := range d.Steps {
			if s.StepID == "c" && s.BlockCode != nil && *s.BlockCode == "task_resource_busy" {
				blocked = true
			}
		}
		if !blocked {
			t.Fatal("missing resource blocker", sameMember)
		}
		taskFinish(t, db, taskAttemptFor(t, db, run.ID, "b"), "succeeded")
		if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "c", "worker"); err != nil || op == "" {
			t.Fatal("resource did not recover", err)
		}
	}
	f, created, _, run := newTaskExecution(t, db)
	checks := []struct {
		change string
		args   []any
		code   string
	}{
		{"UPDATE servers SET last_seen_at=? WHERE id=?", []any{time.Now().UTC().Add(-time.Hour), f.server}, "server_offline"},
		{"UPDATE servers SET last_seen_at=? WHERE id=?", []any{time.Now().UTC(), f.server}, ""},
		{"UPDATE server_engines SET credential_profile_id=NULL WHERE server_id=? AND engine='codex'", []any{f.server}, "credentials_missing"},
	}
	for _, c := range checks {
		if _, err := db.DB.Exec(db.Q(c.change), c.args...); err != nil {
			t.Fatal(err)
		}
		if c.code == "" {
			continue
		}
		if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil || op != "" {
			t.Fatal("unavailable resource dispatched", c.code, err)
		}
		var code string
		if err := db.DB.Get(&code, db.Q("SELECT block_code FROM task_step_runs WHERE run_id=? AND step_id='a'"), run.ID); err != nil || code != c.code {
			t.Fatal(code, c.code, err)
		}
	}
	// Budget exhaustion persists, independent of worker identity.
	if _, err := db.DB.Exec(db.Q("UPDATE tasks SET counters_json=? WHERE id=?"), `{"attempts":100,"runs":1,"plans":1}`, created.ID); err != nil {
		t.Fatal(err)
	}
	if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "replacement"); err != nil || op != "" {
		t.Fatal("budget dispatched", err)
	}
	var code string
	if err := db.DB.Get(&code, db.Q("SELECT block_code FROM task_step_runs WHERE run_id=? AND step_id='a'"), run.ID); err != nil || code != "attempt_budget" {
		t.Fatal(code, err)
	}
}

func assertTaskActionDispatchRace(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	for _, action := range []string{"pause", "cancel"} {
		f, created, _, run := newTaskExecution(t, db)
		barrier := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-barrier
			_, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker")
			results <- err
		}()
		go func() {
			<-barrier
			_, err := db.ActOnTaskRun(ctx, f.project, created.ID, run.ID, task.Action{Meta: task.Meta{Actor: "task-user", RequestID: "race-" + action, ExpectedVersion: run.Version}, Action: action, Reason: "explicit concurrent action"})
			results <- err
		}()
		close(barrier)
		for i := 0; i < 2; i++ {
			if err := <-results; err != nil {
				t.Fatal(action, err)
			}
		}
		var before int
		if err := db.DB.Get(&before, db.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=?"), run.ID); err != nil || before > 1 {
			t.Fatal(before, err)
		}
		if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
			t.Fatal(err)
		}
		var after int
		if err := db.DB.Get(&after, db.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=?"), run.ID); err != nil || before != after {
			t.Fatal("action allowed later dispatch", action, before, after, err)
		}
	}
}

func assertTaskExecutionCommandRollback(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	f, created, plan, run := newTaskExecution(t, db)
	d, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Grant
	grant := task.GrantInput{Meta: task.Meta{Actor: "task-user", RequestID: "rollback-grant", ExpectedVersion: d.Task.Version}, Enabled: true, AllowedMembers: g.AllowedMembers, WorkspaceIDs: g.WorkspaceIDs, ExecutionModes: g.ExecutionModes, Budgets: g.Budgets}
	for _, table := range []string{"task_grant_revisions", "events", "task_requests"} {
		before := taskCount(t, db, "task_grant_revisions")
		remove := injectTaskFailure(t, db, table)
		_, err := db.SetTaskGrant(ctx, f.project, created.ID, grant)
		remove()
		if err == nil || taskCount(t, db, "task_grant_revisions") != before {
			t.Fatal("grant rollback", table, err)
		}
	}
	if _, err := db.SetTaskGrant(ctx, f.project, created.ID, grant); err != nil {
		t.Fatal("grant retry", err)
	}
	action := task.Action{Meta: task.Meta{Actor: "task-user", RequestID: "rollback-action", ExpectedVersion: run.Version}, Action: "pause", Reason: "explicit pause"}
	for _, table := range []string{"events", "task_requests"} {
		remove := injectTaskFailure(t, db, table)
		_, err := db.ActOnTaskRun(ctx, f.project, created.ID, run.ID, action)
		remove()
		d, readErr := db.TaskDetail(ctx, f.project, created.ID)
		if err == nil || readErr != nil || d.Run.State != "running" || d.Run.Version != run.Version {
			t.Fatal("action rollback", table, err, readErr)
		}
	}
	if _, err := db.ActOnTaskRun(ctx, f.project, created.ID, run.ID, action); err != nil {
		t.Fatal("action retry", err)
	}
	// Start rollback is tested on a completed run's task so no concurrent worker
	// can consume the draft while each failure window is injected.
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "cancel"})
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	d, err = db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := task.Start{Meta: task.Meta{Actor: "task-user", RequestID: "rollback-start", ExpectedVersion: d.Task.Version}, PlanID: plan.ID, GrantRevision: d.Grant.Revision}
	for _, table := range []string{"task_runs", "task_step_runs", "events", "task_requests"} {
		before := taskCount(t, db, "task_runs")
		remove := injectTaskFailure(t, db, table)
		_, err := db.StartTaskRun(ctx, f.project, created.ID, start)
		remove()
		if err == nil || taskCount(t, db, "task_runs") != before {
			t.Fatal("start rollback", table, err)
		}
	}
	if _, err := db.StartTaskRun(ctx, f.project, created.ID, start); err != nil {
		t.Fatal("start retry", err)
	}
}

func TestTaskNativeAndCollaborationOwnership(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "ownership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	f, created, _, run := newTaskExecution(t, db)
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	taskFinish(t, db, taskAttemptFor(t, db, run.ID, "a"), "succeeded")
	for _, step := range []string{"b", "c"} {
		if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, step, "worker"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetCollaborationSettings(ctx, sessiontools.Grant{Enabled: true, Worktree: true, AutoContinue: true, TargetModes: map[string]string{"codex": "on-request", "claude": "default", "gemini": "default"}}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"a", "b", "c"} {
		a := taskAttemptFor(t, db, run.ID, step)
		for _, kind := range []string{a.Engine + ".turn.start", a.Engine + ".turn.interrupt", a.Engine + ".thread.fork"} {
			_, err := db.QueueOperation(ctx, f.server, kind, map[string]string{"thread_id": a.SessionID, "source_thread_id": a.SessionID}, NewID())
			if !errors.Is(err, ErrStateConflict) {
				t.Fatal("generic native bypass", kind, err)
			}
		}
		if step == "a" {
			if err := db.ClaimThreadForTurn(ctx, a.SessionID); !errors.Is(err, ErrThreadActive) {
				t.Fatal("scheduled Codex claim bypass", err)
			}
		} else if a.Engine == "claude" {
			thread, err := db.ClaudeThread(ctx, a.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.QueueClaudeTurn(ctx, thread, "scheduled", "sonnet", "default", ""); !errors.Is(err, ErrStateConflict) {
				t.Fatal("scheduled Claude bypass", err)
			}
		} else {
			thread, err := db.GeminiThread(ctx, a.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.QueueGeminiTurn(ctx, thread, "scheduled", "auto", "default"); !errors.Is(err, ErrStateConflict) {
				t.Fatal("scheduled Gemini bypass", err)
			}
		}
		if step == "a" {
			continue
		} // source needs a live native turn.
		reply := db.ExecuteCollaboration(ctx, f.server, sessiontools.Call{Source: session.Ref{Engine: a.Engine, ID: a.SessionID}, OperationID: *a.OperationID, Name: "wio_create_session", Arguments: json.RawMessage(`{"request_id":"bypass","engine":"codex","workspace_id":"` + f.workspace + `","prompt":"bypass"}`)})
		if reply.Error == nil || reply.Error.Code != "STATE_CONFLICT" {
			t.Fatal("task source escaped through global collaboration", reply)
		}
		command := protocol.GitWorktreeCreateCommand{}
		if a.Engine == "claude" {
			command.ClaudeFork = &protocol.ClaudeForkCommand{SourceThreadID: a.SessionID}
		} else {
			command.GeminiFork = &protocol.GeminiForkCommand{SourceThreadID: a.SessionID}
		}
		if _, err := db.QueueOperation(ctx, f.server, protocol.OpGitWorktreeCreate, command, NewID()); !errors.Is(err, ErrStateConflict) {
			t.Fatal("nested worktree fork bypass", a.Engine, err)
		}
	}
	legacy := newContinuationFixture(t, db)
	child := legacy.continuingChild(t, "child-before-task-binding")
	legacy.endTurn(t, child.Session.ID, child.OperationID)
	if _, err := db.DB.Exec(db.Q("INSERT INTO task_session_bindings(id,task_id,run_id,engine,session_id,member_id) VALUES(?,?,?,'codex',?,'legacy')"), NewID(), created.ID, run.ID, legacy.source.ID); err != nil {
		t.Fatal(err)
	}
	legacy.parentEnds(t, "idle")
	if got := legacy.announcements(t); len(got) != 0 {
		t.Fatal("task-owned parent continued automatically", got)
	}

}

func assertTaskDeadlineInterrupt(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	_, created, _, run := newTaskExecution(t, db)
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	a := taskAttemptFor(t, db, run.ID, "a")
	if err := db.MarkDelivered(ctx, *a.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(db.Q("UPDATE task_attempts SET deadline_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), a.ID); err != nil {
		t.Fatal(err)
	}
	fence, ok, err := db.ClaimTaskAttempt(ctx, a.ID, "worker")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := db.ReconcileTaskAttempt(ctx, fence); err != nil {
		t.Fatal(err)
	}
	a = taskAttemptFor(t, db, run.ID, "a")
	if a.State != "unknown" || a.InterruptOperationID != nil {
		t.Fatal("deadline invented Codex turn identity", a.State)
	}
	if _, err := db.DB.Exec(db.Q("UPDATE codex_threads SET codex_thread_id='native-deadline' WHERE id=?"), a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: a.SessionID, Kind: protocol.EventTurnAccepted, Payload: json.RawMessage(`{"turn_id":"deadline-turn"}`)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.ReconcileTaskAttempt(ctx, fence); err != nil {
			t.Fatal(err)
		}
	}
	a = taskAttemptFor(t, db, run.ID, "a")
	if a.State != "unknown" || a.InterruptOperationID == nil {
		t.Fatal("deadline failed to persist interrupt", a.State)
	}
	var count int
	if err := db.DB.Get(&count, db.Q("SELECT COUNT(*) FROM agent_operations WHERE idempotency_key=?"), "task-interrupt:"+a.ID); err != nil || count != 1 {
		t.Fatal("duplicate deadline interrupt", count, err)
	}
	taskFinish(t, db, a, "cancelled")
}

func assertTaskAcceptance(t *testing.T, db *Store, f taskFixture, created task.Task, run task.Run) {
	t.Helper()
	ctx := context.Background()
	snapshot, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || snapshot.AcceptanceEvidenceHash == "" {
		t.Fatal("missing acceptance evidence", err)
	}
	archive := task.Archive{Meta: task.Meta{Actor: "task-user", RequestID: "archive-before-decision", ExpectedVersion: snapshot.Task.Version}}
	archived := true
	archive.Archived = &archived
	_, err = db.ArchiveTask(ctx, f.project, created.ID, archive)
	taskCode(t, err, task.StateConflict)
	in := task.Acceptance{Meta: task.Meta{Actor: "task-user", RequestID: "decision", ExpectedVersion: snapshot.Run.Version}, Decision: "accepted", Reason: "Verified session evidence and task criteria", EvidenceHash: "incorrect"}
	_, err = db.AcceptTaskRun(ctx, f.project, created.ID, run.ID, in)
	taskCode(t, err, task.Conflict)
	in.EvidenceHash = snapshot.AcceptanceEvidenceHash
	remove := injectTaskFailure(t, db, "task_requests")
	_, err = db.AcceptTaskRun(ctx, f.project, created.ID, run.ID, in)
	remove()
	if err == nil {
		t.Fatal("missing ledger failure")
	}
	after, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || after.Run.State != "awaiting_acceptance" || after.AcceptanceEvidenceHash != in.EvidenceHash {
		t.Fatal("partial acceptance", err)
	}
	result, err := db.AcceptTaskRun(ctx, f.project, created.ID, run.ID, in)
	if err != nil || result.State != "accepted" || result.AcceptanceReason == nil || *result.AcceptanceReason != in.Reason {
		t.Fatal(result, err)
	}
	replay, err := db.AcceptTaskRun(ctx, f.project, created.ID, run.ID, in)
	if err != nil || replay.Version != result.Version {
		t.Fatal("decision did not replay", err)
	}
	for _, attempt := range after.Attempts {
		if err := db.taskMutationGuard(ctx, db.DB, attempt.Engine, attempt.SessionID); err != nil {
			t.Fatal("acceptance left session owned", err)
		}
	}
	after, err = db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	archive.ExpectedVersion = after.Task.Version
	saved, err := db.ArchiveTask(ctx, f.project, created.ID, archive)
	if err != nil || saved.ArchivedAt == nil || saved.State != "accepted" {
		t.Fatal("archive lost acceptance", err)
	}
	archived = false
	archive.ExpectedVersion = saved.Version
	archive.RequestID = "restore-after-acceptance"
	restored, err := db.ArchiveTask(ctx, f.project, created.ID, archive)
	if err != nil || restored.ArchivedAt != nil || restored.State != "accepted" {
		t.Fatal("restore lost acceptance", err)
	}
	change := task.AppendPlan{Meta: task.Meta{Actor: "task-user", RequestID: "plan-after-acceptance", ExpectedVersion: restored.Version}, ParentID: &after.Plan.ID, Members: after.Plan.Members, Steps: after.Plan.Steps, Reason: "Prepare a later run"}
	if _, err := db.AppendTaskPlan(ctx, f.project, created.ID, change); err != nil {
		t.Fatal(err)
	}
	last, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || last.Plan.ID == after.Plan.ID || last.RunPlan == nil || last.RunPlan.ID != after.Plan.ID || last.Run.State != "accepted" {
		t.Fatal("previous run lost its original plan", err)
	}
}

func TestTaskRetriesUncapturedArtifactAndCancelsBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	f, created, _, run := newTaskExecutionPlan(t, db, func(plan *task.AppendPlan) {
		plan.Steps[0].Kind = "develop"
		plan.Steps[0].CompletionPolicy = "artifact"
	})
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	first := taskAttemptFor(t, db, run.ID, "a")
	taskFinish(t, db, first, "succeeded")
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "retry_step", StepID: "a"})
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	second := taskAttemptFor(t, db, run.ID, "a")
	if second.Number != 2 || second.ID == first.ID {
		t.Fatalf("retry did not allocate a new attempt: %+v", second)
	}
	taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "cancel"})
	if err := db.AdvanceTasks(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimCodexTurnDelivery(ctx, *second.OperationID)
	if err != nil || claimed {
		t.Fatalf("cancelled operation was still dispatchable: %v %v", claimed, err)
	}
	detail, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || detail.Run.State != "cancelled" {
		t.Fatalf("undelivered run did not reach cancelled: %+v %v", detail.Run, err)
	}
}
