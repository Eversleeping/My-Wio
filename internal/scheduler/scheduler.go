package scheduler

import (
	"context"
	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/schedule"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessionservice"
	"github.com/wio-platform/wio/internal/store"
	"log/slog"
	"time"
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
			// The task cannot advance, so it would stay due forever and
			// enough such tasks would fill every batch and starve valid ones.
			disabled, err := r.store.DisableScheduledTask(ctx, task.ID, task.NextRunAt, "task disabled: "+nextErr.Error(), now)
			if err != nil {
				r.log.Warn("could not disable scheduled task with an invalid schedule", "task_id", task.ID, "error", err)
			} else if disabled {
				r.log.Warn("disabled scheduled task with an invalid schedule", "task_id", task.ID, "error", nextErr)
			}
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
	registry := sessionservice.NewDrivers(r.store, r.gateway.Wake, r.log)
	driver, err := registry.Driver(task.AgentKind)
	if err != nil {
		driver, _ = registry.Driver(session.Codex)
	} // preserve legacy empty/unknown fallback
	driver.EnqueueScheduled(ctx, task, now)
}
