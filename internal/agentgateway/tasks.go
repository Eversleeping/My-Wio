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
	broadcast := map[string]int64{}
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
		var tasks []struct {
			ID       string `db:"id"`
			Sequence int64  `db:"sequence"`
		}
		if err := g.store.DB.SelectContext(ctx, &tasks, g.store.Q("SELECT t.id,MAX(e.sequence) AS sequence FROM tasks t JOIN events e ON e.stream_id='task:'||t.id WHERE e.occurred_at>=? GROUP BY t.id"), since); err == nil {
			seen := make(map[string]int64, len(tasks))
			for _, task := range tasks {
				if task.Sequence > broadcast[task.ID] {
					g.hub.Publish(protocol.StreamEvent{StreamID: "task:" + task.ID, Kind: "task.run.state.changed", OccurredAt: started})
				}
				seen[task.ID] = max(task.Sequence, broadcast[task.ID])
			}
			broadcast = seen
			since = started.Add(-10 * time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
