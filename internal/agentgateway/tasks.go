package agentgateway

import (
	"context"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"github.com/wio-platform/wio/internal/taskservice"
)

func (g *Gateway) RunTasks(ctx context.Context) {
	service := taskservice.Service{Repository: g.store}
	owner := store.NewID()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	since := time.Now().UTC().Add(-10 * time.Second)
	for {
		if err := service.Advance(ctx, owner); err != nil && ctx.Err() == nil {
			g.log.Warn("task reconciliation failed", "error", err)
		}
		// Transactions have completed before any gateway wake or broadcast.
		var servers []string
		if err := g.store.DB.SelectContext(ctx, &servers, `SELECT DISTINCT a.server_id FROM task_attempts a JOIN agent_operations o ON o.id=a.operation_id OR o.id=a.interrupt_operation_id WHERE o.status IN ('queued','delivered','running')`); err == nil {
			for _, server := range servers {
				g.Wake(server)
			}
		}
		started := time.Now().UTC()
		var tasks []string
		if err := g.store.DB.SelectContext(ctx, &tasks, g.store.Q("SELECT DISTINCT t.id FROM tasks t JOIN events e ON e.stream_id='task:'||t.id WHERE e.occurred_at>=?"), since); err == nil {
			for _, id := range tasks {
				g.hub.Publish(protocol.StreamEvent{StreamID: "task:" + id, Kind: "task.run.state.changed", OccurredAt: started})
			}
			since = started.Add(-10 * time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
