package sessionservice

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
)

type claudeDriver struct{ *driverBase }

func (r *claudeDriver) Engine() session.Engine { return session.Claude }

func (r *claudeDriver) Session(ctx context.Context, id string) (session.Record, error) {
	return r.store.AISession(ctx, session.Ref{Engine: r.Engine(), ID: id})
}

func (r *claudeDriver) DefaultScheduleMode() string { return "default" }

func (r *claudeDriver) EnqueueScheduled(ctx context.Context, task store.ScheduledTask, now time.Time) {
	t, err := r.store.ClaudeThread(ctx, task.ThreadID)
	if err != nil {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", "Claude session no longer exists", "", now)
		return
	}
	if t.ServerStatus != "online" || t.ArchivedAt != nil || t.RecoveryPending || t.Status == "queued" || t.Status == "running" || t.Status == "interrupting" {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "skipped", "Claude session is offline, archived or active", "", now)
		return
	}
	if !buildinfo.SupportsClaudeInput(t.AgentVersion) {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", "update Claude Agent to "+buildinfo.MinimumClaudeInputVersion, "", now)
		return
	}
	model, mode := task.Model, task.ApprovalMode
	if model == "" {
		model = t.Model
	}
	if mode == "" {
		mode = t.PermissionMode
	}
	if _, err := r.store.QueueScheduledClaudeTurn(ctx, task.ID, t, task.Prompt, model, mode, task.ReasoningEffort, now); err != nil {
		r.markQueueError(ctx, task, "Claude", err, now)
		return
	}
	r.wake(t.ServerID)
}

func (r *claudeDriver) ValidateSchedule(ctx context.Context, input session.ScheduledTaskInput) error {
	threadID, model, reasoning, approval, level := input.ThreadID, input.Model, input.ReasoningEffort, input.ApprovalMode, input.ThinkingLevel
	_ = threadID
	_ = model
	_ = reasoning
	_ = approval
	_ = level
	if err := validateScheduleLevel(input); err != nil {
		return err
	}
	if reasoning != "" && !protocol.ValidClaudeEffort(reasoning) {
		return errors.New("invalid reasoning_effort")
	}
	thread, err := r.store.ClaudeThread(ctx, threadID)
	if err != nil {
		return session.LookupError{Err: err}
	}
	if !buildinfo.SupportsClaudeInput(thread.AgentVersion) {
		return errors.New("update Claude Agent to " + buildinfo.MinimumClaudeInputVersion + " or newer")
	}
	selectedModel := model
	if selectedModel == "" {
		selectedModel = thread.Model
	}
	baseModel := strings.TrimSuffix(selectedModel, "[1m]")
	valid := len(selectedModel) <= 128 && scheduleModelPattern.MatchString(baseModel) && (approval == "default" || approval == "acceptEdits" || approval == "plan" || approval == "bypassPermissions")
	if !valid || !protocol.ValidClaudeEffort(reasoning) {
		return errors.New("invalid Claude model, effort or permission mode")
	}
	return nil
}
