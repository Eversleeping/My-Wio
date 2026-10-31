package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
)

func TestGeminiGoalReportRequiresCurrentOperationAndNormalCompletion(t *testing.T) {
	db, server := gatewayTestServer(t, "goal-agent")
	ctx := context.Background()
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.84", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/goal", Name: "goal"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	goal := &protocol.GeminiGoalCommand{Objective: "Complete all tests", MaxContinuations: 3, MaxSeconds: 120}
	if _, err := db.QueueGeminiGoal(ctx, thread, goal.Objective, "flash", "default", "", nil, goal); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	send := func(operationID, report string, count int) error {
		payload, _ := json.Marshal(protocol.GeminiGoalProgress{GoalID: goal.ID, OperationID: operationID, Continuations: count, Report: report, Evidence: "Tests passed"})
		raw, _ := json.Marshal(protocol.StreamEvent{EventID: operationID + report, StreamID: thread.ID, Kind: "gemini.goal.progress", Payload: payload})
		return g.handleGeminiEvent(ctx, server.ID, raw)
	}
	if err := send("old-operation", "completed", 99); err != nil {
		t.Fatal(err)
	}
	if err := send(thread.OperationID, "completed", 2); err != nil {
		t.Fatal(err)
	}
	current, _ := db.GeminiGoal(ctx, thread.ID)
	if current.Status != "active" || current.Continuations != 2 || current.Evidence != "Tests passed" {
		t.Fatalf("report finalized early or included stale progress: %+v", current)
	}
	if err := db.MarkDelivered(ctx, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(protocol.OperationResult{OperationID: thread.OperationID, Status: "succeeded"})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
		t.Fatal(err)
	}
	current, _ = db.GeminiGoal(ctx, thread.ID)
	if current.Status != "completed" {
		t.Fatalf("normal completion not saved: %+v", current)
	}
}

func TestGeminiGoalPauseResumeAndUnreportedCompletion(t *testing.T) {
	db, server := gatewayTestServer(t, "goal-pause-agent")
	ctx := context.Background()
	_ = db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.84", GeminiVersion: "0.66.0"})
	_ = db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/goal-pause", Name: "goal"}}})
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	goal := &protocol.GeminiGoalCommand{Objective: "Original objective", MaxContinuations: 2, MaxSeconds: 120}
	if _, err := db.QueueGeminiGoal(ctx, thread, goal.Objective, "flash", "default", "", nil, goal); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	oldOp := thread.OperationID
	if err := db.UpdateGeminiGoalProgress(ctx, thread.ID, protocol.GeminiGoalProgress{GoalID: goal.ID, OperationID: oldOp, Continuations: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.InterruptGeminiTurn(ctx, thread); err != nil {
		t.Fatal(err)
	}
	current, _ := db.GeminiGoal(ctx, thread.ID)
	if current.Status != "paused" || current.Continuations != 1 {
		t.Fatalf("interrupt did not pause goal: %+v", current)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	resume := &protocol.GeminiGoalCommand{ID: goal.ID, MaxContinuations: 4, MaxSeconds: 300}
	if _, err := db.QueueGeminiGoal(ctx, thread, "Continue", "flash", "default", "", nil, resume); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	if resume.Objective != goal.Objective || resume.Continuations != 1 || thread.OperationID == oldOp {
		t.Fatalf("resume lost goal identity or progress: %+v", resume)
	}
	_ = db.UpdateGeminiGoalProgress(ctx, thread.ID, protocol.GeminiGoalProgress{GoalID: goal.ID, OperationID: oldOp, Continuations: 99, Report: "completed", Evidence: "stale"})
	if err := db.CompleteGeminiTurn(ctx, thread.ID, thread.OperationID, "idle"); err != nil {
		t.Fatal(err)
	}
	current, _ = db.GeminiGoal(ctx, thread.ID)
	if current.Status != "blocked" || current.Continuations != 1 || current.Evidence == "stale" {
		t.Fatalf("unreported completion or stale event accepted: %+v", current)
	}
}

func TestGeminiGoalFailedStopAndRecoveredOwnerRestoreActiveStatus(t *testing.T) {
	for _, action := range []string{"paused", "cancelled"} {
		for _, lostOwner := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "-failure", true: "-ownership"}[lostOwner], func(t *testing.T) {
				ctx := context.Background()
				db, server := gatewayTestServer(t, "goal-stop-"+action)
				_ = db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.84", GeminiVersion: "0.66.0"})
				_ = db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/goal-stop", Name: "goal"}}})
				workspaces, _ := db.ListWorkspaces(ctx)
				thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
				if err != nil {
					t.Fatal(err)
				}
				goal := &protocol.GeminiGoalCommand{Objective: "Finish safely", MaxContinuations: 2, MaxSeconds: 120}
				if _, err := db.QueueGeminiGoal(ctx, thread, goal.Objective, "flash", "default", "", nil, goal); err != nil {
					t.Fatal(err)
				}
				thread, _ = db.GeminiThread(ctx, thread.ID)
				if err := db.MarkDelivered(ctx, thread.OperationID); err != nil {
					t.Fatal(err)
				}
				if err := db.InterruptGeminiTurn(ctx, thread, action, goal.ID); err != nil {
					t.Fatal(err)
				}
				// The process can finish reporting while its stop request is in flight.
				if err := db.UpdateGeminiGoalProgress(ctx, thread.ID, protocol.GeminiGoalProgress{GoalID: goal.ID, OperationID: thread.OperationID, Continuations: 1, Report: "completed", Evidence: "Tests passed"}); err != nil {
					t.Fatal(err)
				}
				pending, err := db.PendingOperations(ctx, server.ID)
				if err != nil {
					t.Fatal(err)
				}
				interruptID := ""
				for _, operation := range pending {
					if operation.Kind == "gemini.turn.interrupt" {
						interruptID = operation.ID
					}
				}
				if interruptID == "" {
					t.Fatal("missing interrupt")
				}
				g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
				message := "stop failed"
				if lostOwner {
					message = protocol.GeminiOwnershipLostMessage
				}
				result, _ := json.Marshal(protocol.OperationResult{OperationID: interruptID, Status: "failed", Message: message})
				if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
					t.Fatal(err)
				}
				if lostOwner {
					current, _ := db.GeminiThread(ctx, thread.ID)
					if !current.RecoveryPending {
						t.Fatalf("lost process was not kept reserved: %+v", current)
					}
					if err := g.markGeminiOwnershipActive(ctx, server.ID, thread.OperationID); err != nil {
						t.Fatal(err)
					}
				}
				current, _ := db.GeminiThread(ctx, thread.ID)
				stored, _ := db.GeminiGoal(ctx, thread.ID)
				if current.Status != "running" || stored.Status != "active" || stored.Report != "completed" {
					t.Fatalf("live goal not restored: thread=%+v goal=%+v", current, stored)
				}
				if err := db.CompleteGeminiTurn(ctx, thread.ID, thread.OperationID, "idle"); err != nil {
					t.Fatal(err)
				}
				stored, _ = db.GeminiGoal(ctx, thread.ID)
				if stored.Status != "completed" {
					t.Fatalf("buffered completion report was lost: %+v", stored)
				}
			})
		}
	}
}

func TestGeminiGoalResumePreservesImagesAndFailedReplacementKeepsGoal(t *testing.T) {
	ctx := context.Background()
	db, server := gatewayTestServer(t, "goal-images")
	_ = db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.84", GeminiVersion: "0.66.0"})
	_ = db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/goal-images", Name: "goal"}}})
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	images := []protocol.TurnImage{{DataURL: "data:image/png;base64,aW1hZ2U="}}
	goal := &protocol.GeminiGoalCommand{Objective: "Implement the attached design", MaxContinuations: 2, MaxSeconds: 120}
	if _, err := db.QueueGeminiGoal(ctx, thread, goal.Objective, "flash", "default", "", images, goal); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	if err := db.InterruptGeminiTurn(ctx, thread); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	other, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueGeminiTurn(ctx, other, "Occupy workspace", "flash", "default"); err != nil {
		t.Fatal(err)
	}
	replacement := &protocol.GeminiGoalCommand{Objective: "New goal", MaxContinuations: 2, MaxSeconds: 120}
	if _, err := db.QueueGeminiGoal(ctx, thread, replacement.Objective, "flash", "default", "", nil, replacement); err == nil {
		t.Fatal("workspace conflict accepted")
	}
	stored, _ := db.GeminiGoal(ctx, thread.ID)
	if stored.ID != goal.ID || stored.Status != "paused" {
		t.Fatalf("failed replacement changed existing goal: %+v", stored)
	}
	other, _ = db.GeminiThread(ctx, other.ID)
	if err := db.InterruptGeminiTurn(ctx, other); err != nil {
		t.Fatal(err)
	}
	resume := &protocol.GeminiGoalCommand{ID: goal.ID, MaxContinuations: 3, MaxSeconds: 180}
	if _, err := db.QueueGeminiGoal(ctx, thread, "Continue", "flash", "default", "", nil, resume); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	op, err := db.Operation(ctx, thread.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.GeminiTurnCommand
	if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
		t.Fatal(err)
	}
	if len(command.Images) != 1 || command.Images[0].DataURL != images[0].DataURL || command.Goal.Objective != goal.Objective {
		t.Fatalf("resume lost the original input: %+v", command)
	}
	if err := db.InterruptGeminiTurn(ctx, thread); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	replacement = &protocol.GeminiGoalCommand{Objective: "New goal", MaxContinuations: 2, MaxSeconds: 120}
	if _, err := db.QueueGeminiGoal(ctx, thread, replacement.Objective, "flash", "default", "", nil, replacement); err != nil {
		t.Fatal(err)
	}
	stored, _ = db.GeminiGoal(ctx, thread.ID)
	var oldStatus string
	if err := db.DB.GetContext(ctx, &oldStatus, db.Q("SELECT status FROM gemini_goals WHERE id=?"), goal.ID); err != nil {
		t.Fatal(err)
	}
	if stored.ID != replacement.ID || stored.Status != "active" || oldStatus != "cancelled" {
		t.Fatalf("replacement: latest=%+v old=%s", stored, oldStatus)
	}
}
