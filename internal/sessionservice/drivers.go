package sessionservice

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"time"

	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
)

type driverBase struct {
	store *store.Store
	wake  func(string)
	log   *slog.Logger
}

func NewDrivers(db *store.Store, wake func(string), log *slog.Logger) *session.Registry {
	if wake == nil {
		wake = func(string) {}
	}
	if log == nil {
		log = slog.Default()
	}
	base := &driverBase{db, wake, log}
	return session.NewRegistry(&codexDriver{base}, &geminiDriver{base}, &claudeDriver{base})
}

// markQueueError records why a Claude or Gemini turn was not queued. A busy
// session, workspace or server skips this run; anything else, such as a
// database error, is a failure.
func (r *driverBase) markQueueError(ctx context.Context, task store.ScheduledTask, agent string, err error, now time.Time) {
	reason := ""
	switch {
	case errors.Is(err, store.ErrThreadActive):
		reason = agent + " session or workspace is busy"
	case errors.Is(err, store.ErrWorkspaceWriteActive):
		reason = "workspace has another active write operation"
	case errors.Is(err, store.ErrClaudeCredentialsBusy), errors.Is(err, store.ErrGeminiCredentialsBusy):
		reason = agent + " credentials, CLI installation or Agent update in progress"
	}
	if reason != "" {
		_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "skipped", reason, "", now)
		return
	}
	r.log.Warn("could not queue scheduled turn", "task_id", task.ID, "agent", agent, "error", err)
	_ = r.store.MarkScheduledTaskRun(ctx, task.ID, "failed", "could not queue "+agent+" turn: "+err.Error(), "", now)
}

func validateScheduleLevel(input session.ScheduledTaskInput) error {
	if input.ThinkingLevel != "" {
		return errors.New("thinking_level is only supported for Gemini")
	}
	return nil
}

func validScheduleEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	}
	return false
}

var scheduleModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
