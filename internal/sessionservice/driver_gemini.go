package sessionservice

import (
	"context"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/geminiapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
)

type geminiDriver struct{ *driverBase }

func (r *geminiDriver) Engine() session.Engine { return session.Gemini }

func (r *geminiDriver) Session(ctx context.Context, id string) (session.Record, error) {
	return r.store.AISession(ctx, session.Ref{Engine: r.Engine(), ID: id})
}

func (r *geminiDriver) DefaultScheduleMode() string { return "default" }

func (r *geminiDriver) EnqueueScheduled(ctx context.Context, task store.ScheduledTask, now time.Time) {
	thread, err := r.store.GeminiThread(ctx, task.ThreadID)
	if err != nil {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", "Gemini session no longer exists", "", now)
		return
	}
	if thread.ServerStatus != "online" || thread.ArchivedAt != nil || thread.Status == "queued" || thread.Status == "running" || thread.Status == "interrupting" {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "skipped", "Gemini session is offline, archived or active", "", now)
		return
	}
	if !buildinfo.SupportsGemini(thread.AgentVersion) {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", "update Gemini Agent first", "", now)
		return
	}
	model := task.Model
	if model == "" {
		model = thread.Model
	}
	mode := task.ApprovalMode
	if mode == "" {
		mode = thread.ApprovalMode
	}
	if mode == "yolo" && !buildinfo.SupportsGeminiYolo(thread.AgentVersion) {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", "update Gemini Agent to "+buildinfo.MinimumGeminiYoloVersion+" or newer to use Gemini without approvals", "", now)
		return
	}
	level := task.ThinkingLevel
	if level == "" {
		level = thread.ThinkingLevel
		if !geminiapi.ValidThinkingLevel(model, level) {
			level = ""
		}
	}
	if level != "" && !buildinfo.SupportsGeminiThinking(thread.AgentVersion) {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", "update Gemini Agent to configure thinking", "", now)
		return
	}
	// The run is recorded in the queueing transaction: a result that arrives
	// right after the commit must find the task, and nothing may reset the
	// run to "queued" afterwards.
	if _, err := r.store.QueueScheduledGeminiTurn(ctx, task.ID, thread, task.Prompt, model, mode, level, now); err != nil {
		r.markQueueError(ctx, task, "Gemini", err, now)
		return
	}
	r.wake(thread.ServerID)
}

func (r *geminiDriver) ValidateSchedule(ctx context.Context, input session.ScheduledTaskInput) error {
	threadID, model, reasoning, approval, level := input.ThreadID, input.Model, input.ReasoningEffort, input.ApprovalMode, input.ThinkingLevel
	_ = threadID
	_ = model
	_ = reasoning
	_ = approval
	_ = level
	if reasoning != "" {
		return errors.New("Gemini does not support Codex reasoning_effort")
	}
	if approval != "default" && approval != "auto_edit" && approval != "yolo" {
		return errors.New("invalid approval_mode")
	}
	thread, err := r.store.GeminiThread(ctx, threadID)
	if err != nil {
		return session.LookupError{Err: err}
	}
	if approval == "yolo" && !buildinfo.SupportsGeminiYolo(thread.AgentVersion) {
		return errors.New("update Gemini Agent to " + buildinfo.MinimumGeminiYoloVersion + " or newer to use Gemini without approvals")
	}
	selectedModel := model
	if selectedModel == "" {
		selectedModel = thread.Model
	}
	if !geminiapi.ValidThinkingLevel(selectedModel, level) {
		return errors.New("invalid Gemini thinking level for this model")
	}
	if level != "" {
		server, e := r.store.Server(ctx, thread.ServerID)
		if e != nil {
			return session.LookupError{Err: e}
		}
		if !buildinfo.SupportsGeminiThinking(server.AgentVersion) {
			return errors.New("update Gemini Agent to configure thinking")
		}
	}
	if model != "" && !scheduleModelPattern.MatchString(model) {
		return errors.New("invalid Gemini model")
	}
	return nil
}

func (r *geminiDriver) Native(ctx context.Context, id string) (any, error) {
	return r.store.GeminiThread(ctx, id)
}

func (r *geminiDriver) Events(ctx context.Context, id string, q session.EventQuery) ([]protocol.StreamEvent, error) {
	return r.driverBase.Events(ctx, id, q)
}
