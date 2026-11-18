package sessionservice

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
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
	if _, err := r.store.QueueScheduledCodexTurn(ctx, task.ID, thread, command, now); err != nil {
		if errors.Is(err, store.ErrThreadActive) {
			_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "skipped", "Codex session is already active", "", now)
		} else {
			_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", err.Error(), "", now)
		}
		return
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
		return session.LookupError{Err: err}
	}
	return nil
}

func (r *codexDriver) Native(ctx context.Context, id string) (any, error) {
	return r.store.Thread(ctx, id)
}

func (r *codexDriver) Events(ctx context.Context, id string, q session.EventQuery) ([]protocol.StreamEvent, error) {
	if q.Raw {
		return r.driverBase.Events(ctx, id, q)
	}
	if q.After != nil {
		return r.store.ConversationEvents(ctx, id, *q.After, q.Limit)
	}
	if q.Before != nil {
		return r.store.ConversationEventsBefore(ctx, id, *q.Before, q.Limit)
	}
	return r.store.RecentConversationEvents(ctx, id, q.Limit)
}
