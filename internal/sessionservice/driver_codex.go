package sessionservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
	"strings"
	"time"
)

type codexDriver struct{ *driverBase }

func (r *codexDriver) Engine() session.Engine { return session.Codex }

func (r *codexDriver) Session(ctx context.Context, id string) (session.Record, error) {
	return r.store.AISession(ctx, session.Ref{Engine: r.Engine(), ID: id})
}

func (r *codexDriver) DefaultScheduleMode() string { return "on-request" }

func (r *codexDriver) EnqueueScheduled(ctx context.Context, task store.ScheduledTask, now time.Time) {
	if task.ServerStatus != "online" {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "skipped", "server is offline", "", now)
		return
	}
	thread, err := r.store.Thread(ctx, task.ThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", "Codex session no longer exists", "", now)
		return
	}
	if err != nil {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", err.Error(), "", now)
		return
	}
	if thread.ArchivedAt != nil {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "skipped", "Codex session is archived", "", now)
		return
	}
	if err := r.store.ClaimThreadForTurn(ctx, thread.ID); err != nil {
		if errors.Is(err, store.ErrThreadActive) {
			_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "skipped", "Codex session is already active", "", now)
		} else {
			_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", err.Error(), "", now)
		}
		return
	}
	command := protocol.StartTurnCommand{
		ThreadID:        thread.ID,
		CodexThread:     thread.CodexThreadID,
		WorkspaceID:     thread.WorkspaceID,
		Workspace:       thread.Path,
		Prompt:          task.Prompt,
		Model:           task.Model,
		ReasoningEffort: task.ReasoningEffort,
		ApprovalMode:    normalizedApprovalMode(task.ApprovalMode),
	}
	operationID, err := r.store.QueueResourceOperation(ctx, thread.ServerID, protocol.OpCodexTurnStart, command, "codex-scheduled:"+task.ID+":"+now.Format(time.RFC3339Nano), store.OperationResource{ThreadID: thread.ID}, false)
	if err != nil {
		_ = r.store.SetThreadStatus(ctx, thread.ID, "idle")
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", err.Error(), "", now)
		return
	}
	payload, _ := json.Marshal(map[string]any{"text": task.Prompt, "scheduled_task_id": task.ID, "operation_id": operationID})
	if _, eventErr := r.store.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: protocol.EventUserMessage, Payload: payload}); eventErr != nil {
		r.log.Warn("could not record scheduled Codex prompt", "task_id", task.ID, "error", eventErr)
		_, _ = r.store.CancelOperation(ctx, operationID, "could not record scheduled Codex prompt")
		_ = r.store.SetThreadStatus(ctx, thread.ID, "idle")
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", eventErr.Error(), operationID, now)
		return
	}
	if err := r.store.MarkScheduledCodexRunQueued(ctx, task.ID, operationID, now); err != nil {
		r.log.Warn("could not record scheduled Codex task run", "task_id", task.ID, "error", err)
	}
	r.wake(thread.ServerID)
}

func normalizedApprovalMode(value string) string {
	value = strings.TrimSpace(value)
	if value == "untrusted" || value == "never" {
		return value
	}
	return "on-request"
}

func (r *codexDriver) ValidateSchedule(ctx context.Context, input session.ScheduledTaskInput) error {
	threadID, model, reasoning, approval, level := input.ThreadID, input.Model, input.ReasoningEffort, input.ApprovalMode, input.ThinkingLevel
	_ = threadID
	_ = model
	_ = reasoning
	_ = approval
	_ = level
	if err := validateScheduleLevel(input); err != nil {
		return err
	}
	if !validScheduleEffort(reasoning) {
		return errors.New("invalid reasoning_effort")
	}
	if approval != "on-request" && approval != "untrusted" && approval != "never" {
		return errors.New("invalid approval_mode")
	}
	if _, err := r.store.Thread(ctx, threadID); err != nil {
		return session.LookupError{err}
	}
	return nil
}
