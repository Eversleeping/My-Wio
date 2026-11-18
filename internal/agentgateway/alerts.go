package agentgateway

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"time"
)

func (g *Gateway) evaluateMetrics(ctx context.Context, serverID string, m protocol.Metrics) {
	checks := map[string]struct {
		hit      bool
		title    string
		severity string
	}{"cpu_high": {m.CPUPercent >= 90, "CPU usage above 90%", "warning"}, "memory_high": {m.MemoryPercent >= 90, "Memory usage above 90%", "warning"}, "disk_high": {m.DiskPercent >= 90, "Disk usage above 90%", "critical"}}
	for kind, check := range checks {
		g.recordMetricBreach(ctx, serverID, kind, check.hit, check.title, check.severity, "")
	}
}

func (g *Gateway) evaluateOperationLatency(ctx context.Context, serverID string, operation store.Operation) {
	if operation.DeliveredAt == nil {
		return
	}
	queueWait := operation.DeliveredAt.Sub(operation.CreatedAt)
	start := operation.StartedAt
	if start == nil {
		start = operation.DeliveredAt
	}
	execution := time.Duration(0)
	if operation.CompletedAt != nil {
		execution = operation.CompletedAt.Sub(*start)
	} else {
		execution = time.Since(*start)
	}
	checks := map[string]struct {
		hit      bool
		title    string
		severity string
	}{
		"agent_queue_latency":     {queueWait >= 2*time.Second, "Agent queue wait above 2 seconds", "warning"},
		"agent_execution_latency": {execution >= 30*time.Second, "Agent execution above 30 seconds", "warning"},
	}
	detail := fmt.Sprintf("operation=%s queue_wait_ms=%d execution_ms=%d", operation.Kind, queueWait.Milliseconds(), execution.Milliseconds())
	for kind, check := range checks {
		g.recordMetricBreach(ctx, serverID, kind, check.hit, check.title, check.severity, detail)
	}
}

// Gateway connection handling already serializes messages for each server.
// Keep the global lock scoped to the in-memory counters.
func (g *Gateway) recordMetricBreach(ctx context.Context, serverID, kind string, hit bool, title, severity, detail string) {
	g.metricMu.Lock()
	counters := g.metricBreaches[serverID]
	if counters == nil {
		counters = map[string]int{}
		g.metricBreaches[serverID] = counters
	}
	previous, known := counters[kind]
	count := 0
	if hit {
		count = min(previous+1, 3)
	}
	counters[kind] = count
	g.metricMu.Unlock()

	switch {
	case !hit && (!known || previous > 0):
		if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE alerts SET status='resolved',resolved_at=? WHERE server_id=? AND kind=? AND status='open'"), time.Now().UTC(), serverID, kind); err != nil {
			g.metricMu.Lock()
			delete(g.metricBreaches[serverID], kind)
			g.metricMu.Unlock()
		}
	case hit && previous < 3 && count == 3:
		_, err := g.store.DB.ExecContext(ctx, g.store.Q(`INSERT INTO alerts(id,server_id,kind,severity,title,detail)
			SELECT ?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM alerts WHERE server_id=? AND kind=? AND status='open')`),
			uuid.NewString(), serverID, kind, severity, title, detail, serverID, kind)
		if err != nil {
			g.metricMu.Lock()
			if g.metricBreaches[serverID] != nil {
				g.metricBreaches[serverID][kind] = 2
			}
			g.metricMu.Unlock()
		}
	}
}
