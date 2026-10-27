package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/geminiapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/schedule"
	"github.com/wio-platform/wio/internal/store"
)

const defaultInterval = 15 * time.Second

type Runner struct {
	store    *store.Store
	gateway  *agentgateway.Gateway
	log      *slog.Logger
	interval time.Duration
}

func New(database *store.Store, gateway *agentgateway.Gateway, log *slog.Logger) *Runner {
	return &Runner{store: database, gateway: gateway, log: log, interval: defaultInterval}
}

func (r *Runner) Run(ctx context.Context) error {
	r.tick(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	now := time.Now().UTC()
	tasks, err := r.store.DueScheduledTasks(ctx, now, 100)
	if err != nil {
		r.log.Warn("could not load scheduled Codex tasks", "error", err)
		return
	}
	for _, task := range tasks {
		next, nextErr := schedule.Next(task.Schedule, task.Timezone, now)
		if nextErr != nil {
			_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", nextErr.Error(), "", now)
			continue
		}
		claimed, claimErr := r.store.ClaimScheduledTask(ctx, task.ID, task.NextRunAt, next)
		if claimErr != nil || !claimed {
			if claimErr != nil {
				r.log.Warn("could not claim scheduled Codex task", "task_id", task.ID, "error", claimErr)
			}
			continue
		}
		r.enqueue(ctx, task, now)
	}
}

func (r *Runner) enqueue(ctx context.Context, task store.ScheduledTask, now time.Time) {
	if task.AgentKind == "gemini" {
		r.enqueueGemini(ctx, task, now)
		return
	}
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
	operationID, err := r.store.QueueResourceOperation(ctx, thread.ServerID, "codex.turn.start", command, "codex-scheduled:"+task.ID+":"+now.Format(time.RFC3339Nano), store.OperationResource{ThreadID: thread.ID}, false)
	if err != nil {
		_ = r.store.SetThreadStatus(ctx, thread.ID, "idle")
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", err.Error(), "", now)
		return
	}
	payload, _ := json.Marshal(map[string]any{"text": task.Prompt, "scheduled_task_id": task.ID})
	if _, eventErr := r.store.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "user.message", Payload: payload}); eventErr != nil {
		r.log.Warn("could not record scheduled Codex prompt", "task_id", task.ID, "error", eventErr)
		_, _ = r.store.CancelOperation(ctx, operationID, "could not record scheduled Codex prompt")
		_ = r.store.SetThreadStatus(ctx, thread.ID, "idle")
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", eventErr.Error(), operationID, now)
		return
	}
	if err := r.store.MarkScheduledTaskRun(ctx, task.ID, "queued", "", operationID, now); err != nil {
		r.log.Warn("could not record scheduled Codex task run", "task_id", task.ID, "error", err)
	}
	r.gateway.Wake(thread.ServerID)
}

func normalizedApprovalMode(value string) string {
	value = strings.TrimSpace(value)
	if value == "untrusted" || value == "never" {
		return value
	}
	return "on-request"
}

func (r *Runner) enqueueGemini(ctx context.Context, task store.ScheduledTask, now time.Time) {
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
	event, err := r.store.QueueGeminiTurn(ctx, thread, task.Prompt, model, mode, level)
	if err != nil {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "skipped", "Gemini workspace is busy or unavailable", "", now)
		return
	}
	var p struct {
		OperationID string `json:"operation_id"`
	}
	_ = json.Unmarshal(event.Payload, &p)
	_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "queued", "", p.OperationID, now)
	r.gateway.Wake(thread.ServerID)
}
