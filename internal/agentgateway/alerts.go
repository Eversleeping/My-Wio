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
	g.metricMu.Lock()
	defer g.metricMu.Unlock()
	if g.metricBreaches[serverID] == nil {
		g.metricBreaches[serverID] = map[string]int{}
	}
	for kind, check := range checks {
		if check.hit {
			g.metricBreaches[serverID][kind]++
		} else {
			g.metricBreaches[serverID][kind] = 0
			_, _ = g.store.DB.ExecContext(ctx, g.store.Q("UPDATE alerts SET status='resolved',resolved_at=? WHERE server_id=? AND kind=? AND status='open'"), time.Now().UTC(), serverID, kind)
		}
		if g.metricBreaches[serverID][kind] == 3 {
			var n int
			_ = g.store.DB.GetContext(ctx, &n, g.store.Q("SELECT COUNT(*) FROM alerts WHERE server_id=? AND kind=? AND status='open'"), serverID, kind)
			if n == 0 {
				_, _ = g.store.DB.ExecContext(ctx, g.store.Q("INSERT INTO alerts(id,server_id,kind,severity,title) VALUES(?,?,?,?,?)"), uuid.NewString(), serverID, kind, check.severity, check.title)
			}
		}
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
	g.metricMu.Lock()
	defer g.metricMu.Unlock()
	if g.metricBreaches[serverID] == nil {
		g.metricBreaches[serverID] = map[string]int{}
	}
	for kind, check := range checks {
		if !check.hit {
			wasBreaching := g.metricBreaches[serverID][kind] > 0
			g.metricBreaches[serverID][kind] = 0
			if wasBreaching {
				_, _ = g.store.DB.ExecContext(ctx, g.store.Q("UPDATE alerts SET status='resolved',resolved_at=? WHERE server_id=? AND kind=? AND status='open'"), time.Now().UTC(), serverID, kind)
			}
			continue
		}
		g.metricBreaches[serverID][kind]++
		if g.metricBreaches[serverID][kind] != 3 {
			continue
		}
		var open int
		_ = g.store.DB.GetContext(ctx, &open, g.store.Q("SELECT COUNT(*) FROM alerts WHERE server_id=? AND kind=? AND status='open'"), serverID, kind)
		if open == 0 {
			_, _ = g.store.DB.ExecContext(ctx, g.store.Q("INSERT INTO alerts(id,server_id,kind,severity,title,detail) VALUES(?,?,?,?,?,?)"), uuid.NewString(), serverID, kind, check.severity, check.title, fmt.Sprintf("operation=%s queue_wait_ms=%d execution_ms=%d", operation.Kind, queueWait.Milliseconds(), execution.Milliseconds()))
		}
	}
}
