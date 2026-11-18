package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
	"github.com/wio-platform/wio/internal/task"
	"github.com/wio-platform/wio/internal/taskartifact"
)

func TestTaskReviewReworkAndTeam(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertTaskReviewReworkAndTeam(t, db)
}

func TestTaskReviewBudgetSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	restart := func() *Store {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		next, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		db = next
		return next
	}
	assertTaskReviewScenario(t, db, restart, true)
}

func assertTaskReviewReworkAndTeam(t *testing.T, db *Store) {
	t.Helper()
	assertTaskReviewScenario(t, db, nil, false)
}

func assertTaskReviewScenario(t *testing.T, db *Store, restart func() *Store, rejectTwice bool) {
	t.Helper()
	ctx := context.Background()
	f, created, plan, run := newTaskExecutionPlan(t, db, func(p *task.AppendPlan) {
		p.Members[0].Role = "coordinator"
		p.Steps[2].CompletionPolicy = "artifact"
		p.Steps[3].Kind = "review"
		p.Steps[3].CompletionPolicy = "review"
		p.Steps[3].CriterionIDs = []string{"works"}
		p.Steps[3].Inputs = []task.Input{{Kind: "artifact", StepID: "c"}}
	})
	snapshot := func() task.Detail {
		t.Helper()
		d, err := db.TaskDetail(ctx, f.project, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	grant := snapshot().Grant
	budgets := grant.Budgets
	budgets.Reworks = 1
	budgets.Messages = 5
	_, err := db.SetTaskGrant(ctx, f.project, created.ID, task.GrantInput{Meta: task.Meta{Actor: "task-user", RequestID: "enable-scoped-tools", ExpectedVersion: snapshot().Task.Version},
		Enabled: true, TaskTools: true, AllowedMembers: grant.AllowedMembers, WorkspaceIDs: grant.WorkspaceIDs, ExecutionModes: grant.ExecutionModes, Budgets: budgets})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func(step string) task.Attempt {
		t.Helper()
		op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, step, "worker")
		if err != nil || op == "" {
			t.Fatal("dispatch", step, err)
		}
		return taskAttemptFor(t, db, run.ID, step)
	}
	call := func(a task.Attempt, name string, in task.TeamToolInput) sessiontools.Reply {
		raw, _ := json.Marshal(in)
		return db.ExecuteCollaboration(ctx, a.ServerID, sessiontools.Call{Source: sessiontools.Ref{Engine: a.Engine, ID: a.SessionID}, OperationID: *a.OperationID, Name: name, Arguments: raw})
	}
	coordinator := dispatch("a")
	revision := task.TeamPlan{Members: plan.Members, Steps: plan.Steps}
	invalidPlan := task.TeamPlan{Members: append([]task.Member(nil), plan.Members...), Steps: plan.Steps}
	invalidPlan.Members[0].Mode = "nonexistent"
	if invalid := call(coordinator, sessiontools.TaskPlanProposal, task.TeamToolInput{RequestID: "invalid-plan", Reason: "Invalid mode", Plan: &invalidPlan}); invalid.Error == nil || invalid.Error.Code != "INVALID_ARGUMENT" {
		t.Fatal("invalid proposal was accepted", invalid)
	}
	proposal := call(coordinator, sessiontools.TaskPlanProposal, task.TeamToolInput{RequestID: "propose-plan", Reason: "Use an explicit new run", Plan: &revision})
	if proposal.Error != nil {
		t.Fatal(proposal)
	}
	taskFinish(t, db, coordinator, "succeeded")
	research := dispatch("b")
	taskFinish(t, db, research, "succeeded")

	var firstReview task.Review
	var firstArtifact task.Artifact
	for round := 1; round <= 2; round++ {
		developer := dispatch("c")
		if round == 2 {
			op, err := db.Operation(ctx, *developer.OperationID)
			if err != nil || !strings.Contains(op.Payload, "Checked native evidence") || !strings.Contains(op.Payload, "Read the recorded reviewer session evidence") {
				t.Fatal("rework lost the review feedback", err)
			}
			if restart == nil && !strings.Contains(op.Payload, "explicit user action") {
				t.Fatal("rework lost the user reason")
			}
			reply := call(developer, sessiontools.TaskContext, task.TeamToolInput{})
			if reply.Error != nil || reply.Data.(map[string]any)["review_recovery"] == nil {
				t.Fatal("rework feedback is missing from the task context", reply)
			}
		}
		input := task.TeamToolInput{RequestID: "review-once", TargetStepID: "d", Reason: "Confirm the developed version"}
		bad := input
		bad.TargetStepID = "b"
		if invalid := call(developer, sessiontools.TaskReview, bad); invalid.Error == nil {
			t.Fatal("tool widened review scope")
		}
		fake := developer
		fake.SessionID = coordinator.SessionID
		if invalid := call(fake, sessiontools.TaskReview, input); invalid.Error == nil {
			t.Fatal("tool accepted forged source")
		}
		reply := call(developer, sessiontools.TaskReview, input)
		replay := call(developer, sessiontools.TaskReview, input)
		if reply.Error != nil || replay.Error != nil || reply.Data.(task.TeamRequest).ID != replay.Data.(task.TeamRequest).ID {
			t.Fatal("request not stable", reply, replay)
		}
		taskFinish(t, db, developer, "succeeded")
		if op, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "d", "worker"); err != nil || op != "" {
			t.Fatal("review bypassed artifact requirement", err)
		}
		artifact, err := db.CaptureTaskArtifact(ctx, f.project, created.ID, task.CaptureArtifact{Meta: task.Meta{Actor: "task-user", RequestID: NewID(), ExpectedVersion: snapshot().Task.Version}, AttemptID: developer.ID, Paths: []string{"change.txt"}})
		if err != nil {
			t.Fatal(err)
		}
		data := []byte(strings.Repeat("approved content", round))
		manifest := protocol.ArtifactManifest{BaseSHA: strings.Repeat("1", 40), Files: []protocol.ArtifactFile{{Path: "change.txt", AfterHash: taskartifact.Hash(data), AfterMode: "100644", Content: data}}}
		digest, _ := taskartifact.Validate(manifest)
		complete := func(id string, data any) {
			t.Helper()
			op, err := db.Operation(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(data)
			if _, err := db.CompleteTaskArtifact(ctx, op, protocol.OperationResult{OperationID: id, Status: "succeeded", Data: raw}, false); err != nil {
				t.Fatal(err)
			}
		}
		complete(artifact.OperationID, manifest)
		if round == 1 {
			newer, err := db.CaptureTaskArtifact(ctx, f.project, created.ID, task.CaptureArtifact{
				Meta:      task.Meta{Actor: "task-user", RequestID: NewID(), ExpectedVersion: snapshot().Task.Version},
				AttemptID: developer.ID, Paths: []string{"change.txt"},
			})
			if err != nil {
				t.Fatal(err)
			}
			complete(newer.OperationID, manifest)
			artifact = newer
		}
		h, err := db.HandoffTaskArtifact(ctx, f.project, created.ID, artifact.ID, task.HandoffArtifact{Meta: task.Meta{Actor: "task-user", RequestID: NewID(), ExpectedVersion: snapshot().Task.Version},
			WorkspaceID: f.workspace, Digest: digest, Action: "apply"})
		if err != nil {
			t.Fatal(err)
		}
		complete(h.OperationID, protocol.TaskArtifactReceipt{Action: "apply", Digest: digest})
		reviewer := dispatch("d")
		finished := call(reviewer, sessiontools.TaskMessage, task.TeamToolInput{RequestID: "coordinator-note", TargetMemberID: "dev", Reason: "Review result ready"})
		if finished.Error == nil {
			t.Fatal("accepted a self-directed task message")
		}
		taskFinish(t, db, reviewer, "succeeded")
		list, err := db.TaskReviews(ctx, f.project, created.ID)
		var review task.Review
		for _, item := range list {
			if item.AttemptID == reviewer.ID {
				review = item
				break
			}
		}
		if err != nil || len(list) != round || review.EvidenceHash == nil || review.Digest != digest {
			t.Fatal("current review evidence was not recorded", err)
		}
		decision := task.ReviewDecision{Meta: task.Meta{Actor: "task-user", RequestID: NewID(), ExpectedVersion: snapshot().Task.Version}, AttemptID: reviewer.ID, ArtifactID: artifact.ID,
			EvidenceHash: *review.EvidenceHash, Verdict: "approved", Reason: "Checked native evidence", Checks: []task.ReviewCheck{{CriterionID: "works", State: "not_run"}}}
		_, err = db.DecideTaskReview(ctx, f.project, created.ID, decision)
		taskCode(t, err, task.StateConflict)
		decision.Checks[0].Evidence = "Read the recorded reviewer session evidence"
		if round == 1 {
			decision.Verdict, decision.Checks[0].State = "rejected", "failed"
			firstReview, firstArtifact = review, artifact
		} else {
			stale := decision
			stale.AttemptID, stale.ArtifactID, stale.EvidenceHash = firstReview.AttemptID, firstArtifact.ID, *firstReview.EvidenceHash
			_, err := db.DecideTaskReview(ctx, f.project, created.ID, stale)
			taskCode(t, err, task.Conflict)
			decision.Checks[0].State = "passed"
			if rejectTwice {
				decision.Verdict, decision.Checks[0].State = "rejected", "failed"
			}
		}
		if _, err := db.DecideTaskReview(ctx, f.project, created.ID, decision); err != nil {
			t.Fatal(err)
		}
		if round == 1 {
			taskRunAction(t, db, f, created.ID, run.ID, task.Action{Action: "rework", AttemptID: reviewer.ID})
			if restart != nil {
				// Emulate an in-flight recovery queued before recovery_json existed.
				if _, err := db.DB.Exec(db.Q("UPDATE task_step_runs SET recovery_json=NULL WHERE run_id=? AND step_id='c'"), run.ID); err != nil {
					t.Fatal(err)
				}
				db = restart()
			}
		} else if rejectTwice {
			snapshot := snapshot()
			_, err := db.ActOnTaskRun(ctx, f.project, created.ID, run.ID, task.Action{Meta: task.Meta{Actor: "task-user", RequestID: "over-recovery-budget", ExpectedVersion: snapshot.Run.Version}, Action: "rework", AttemptID: reviewer.ID, Reason: "Try once more"})
			taskCode(t, err, task.StateConflict)
			if !strings.Contains(err.Error(), "budget") {
				t.Fatal("recovery was rejected for the wrong reason", err)
			}
			if err := db.AdvanceTasks(ctx, "worker"); err != nil {
				t.Fatal(err)
			}
			detail, err := db.TaskDetail(ctx, f.project, created.ID)
			if err != nil || detail.Run.State != "failed" {
				t.Fatalf("spent rework budget left the run active: %+v %v", detail.Run, err)
			}

			return
		}
	}
	if err := db.projectTaskRun(ctx, created.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	d := snapshot()
	if d.Run.State != "awaiting_acceptance" || d.AcceptanceEvidenceHash == "" {
		t.Fatal("no final acceptance evidence", d.Run)
	}
	_, err = db.AcceptTaskRun(ctx, f.project, created.ID, run.ID, task.Acceptance{Meta: task.Meta{Actor: "task-user", RequestID: "final-user-review", ExpectedVersion: d.Run.Version},
		Decision: "accepted", Reason: "Reviewed the current artifact and checks", EvidenceHash: d.AcceptanceEvidenceHash})
	if err != nil {
		t.Fatal(err)
	}
	requests, err := db.TaskTeam(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fulfilled int
	for _, request := range requests {
		if request.Kind == sessiontools.TaskReview && request.State == "fulfilled" {
			fulfilled++
		}
	}
	if fulfilled != 2 {
		t.Fatal("requests did not bind to dispatched reviews", requests)
	}
	coordinatorProposal := proposal.Data.(task.TeamRequest)
	next, err := db.AppendTaskPlan(ctx, f.project, created.ID, task.AppendPlan{Meta: task.Meta{Actor: "task-user", RequestID: "adopt-plan", ExpectedVersion: snapshot().Task.Version},
		ProposalID: coordinatorProposal.ID, ParentID: &plan.ID, Reason: "Adopt after reviewing plan", Members: revision.Members, Steps: revision.Steps})
	if err != nil || next.Plan.ID == plan.ID {
		t.Fatal("proposal was not adopted", err)
	}
}
