package store

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type OperationMetricAggregate struct {
	Count   int     `json:"count"`
	Average float64 `json:"average_ms"`
	P95     float64 `json:"p95_ms"`
	Max     int64   `json:"max_ms"`
}

type OperationMetrics struct {
	ServerID  string                   `json:"server_id"`
	Since     time.Time                `json:"since"`
	UpdatedAt time.Time                `json:"updated_at"`
	Total     int                      `json:"total"`
	Queued    int                      `json:"queued"`
	Delivered int                      `json:"delivered"`
	Running   int                      `json:"running"`
	Succeeded int                      `json:"succeeded"`
	Failed    int                      `json:"failed"`
	Cancelled int                      `json:"cancelled"`
	QueueWait OperationMetricAggregate `json:"queue_wait"`
	Delivery  OperationMetricAggregate `json:"delivery"`
	Execution OperationMetricAggregate `json:"execution"`
}

func (s *Store) OperationMetrics(ctx context.Context, serverID string, since time.Time) (OperationMetrics, error) {
	var rows []struct {
		Status      string     `db:"status"`
		CreatedAt   time.Time  `db:"created_at"`
		DeliveredAt *time.Time `db:"delivered_at"`
		StartedAt   *time.Time `db:"started_at"`
		CompletedAt *time.Time `db:"completed_at"`
	}
	if err := s.DB.SelectContext(ctx, &rows, s.Q("SELECT status,created_at,delivered_at,started_at,completed_at FROM agent_operations WHERE server_id=? AND created_at>=? ORDER BY created_at"), serverID, since); err != nil {
		return OperationMetrics{}, err
	}
	result := OperationMetrics{ServerID: serverID, Since: since, UpdatedAt: time.Now().UTC()}
	queueWait := make([]int64, 0, len(rows))
	delivery := make([]int64, 0, len(rows))
	execution := make([]int64, 0, len(rows))
	for _, row := range rows {
		result.Total++
		switch row.Status {
		case "queued":
			result.Queued++
		case "delivered":
			result.Delivered++
		case "running":
			result.Running++
		case "succeeded":
			result.Succeeded++
		case "failed", "partial":
			result.Failed++
		case "cancelled", "canceled":
			result.Cancelled++
		}
		if row.DeliveredAt != nil {
			queueWait = append(queueWait, maxDurationMS(row.DeliveredAt.Sub(row.CreatedAt)))
		}
		start := row.StartedAt
		if start == nil {
			start = row.DeliveredAt
		}
		if row.DeliveredAt != nil && start != nil {
			delivery = append(delivery, maxDurationMS(start.Sub(*row.DeliveredAt)))
		}
		if row.CompletedAt != nil && start != nil {
			execution = append(execution, maxDurationMS(row.CompletedAt.Sub(*start)))
		}
	}
	result.QueueWait = aggregateOperationDurations(queueWait)
	result.Delivery = aggregateOperationDurations(delivery)
	result.Execution = aggregateOperationDurations(execution)
	return result, nil
}

func maxDurationMS(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}

func aggregateOperationDurations(values []int64) OperationMetricAggregate {
	if len(values) == 0 {
		return OperationMetricAggregate{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	var total int64
	for _, value := range sorted {
		total += value
	}
	index := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	return OperationMetricAggregate{Count: len(sorted), Average: float64(total) / float64(len(sorted)), P95: float64(sorted[index]), Max: sorted[len(sorted)-1]}
}

func (s *Store) SaveMetrics(ctx context.Context, serverID string, m protocol.Metrics) error {
	now := time.Now().UTC().Truncate(time.Minute)
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO metric_rollups(
		server_id,bucket_at,resolution,cpu_percent,memory_percent,disk_percent,
		memory_used_bytes,memory_total_bytes,disk_used_bytes,disk_total_bytes,
		load_1,net_rx_bytes,net_tx_bytes,samples) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1)
		ON CONFLICT(server_id,bucket_at,resolution) DO UPDATE SET
		cpu_percent=(metric_rollups.cpu_percent*metric_rollups.samples+excluded.cpu_percent)/(metric_rollups.samples+1),
		memory_percent=(metric_rollups.memory_percent*metric_rollups.samples+excluded.memory_percent)/(metric_rollups.samples+1),
		disk_percent=(metric_rollups.disk_percent*metric_rollups.samples+excluded.disk_percent)/(metric_rollups.samples+1),
		memory_used_bytes=CASE WHEN excluded.memory_total_bytes>0 THEN excluded.memory_used_bytes ELSE metric_rollups.memory_used_bytes END,
		memory_total_bytes=CASE WHEN excluded.memory_total_bytes>0 THEN excluded.memory_total_bytes ELSE metric_rollups.memory_total_bytes END,
		disk_used_bytes=CASE WHEN excluded.disk_total_bytes>0 THEN excluded.disk_used_bytes ELSE metric_rollups.disk_used_bytes END,
		disk_total_bytes=CASE WHEN excluded.disk_total_bytes>0 THEN excluded.disk_total_bytes ELSE metric_rollups.disk_total_bytes END,
		load_1=(metric_rollups.load_1*metric_rollups.samples+excluded.load_1)/(metric_rollups.samples+1),
		net_rx_bytes=excluded.net_rx_bytes,net_tx_bytes=excluded.net_tx_bytes,samples=metric_rollups.samples+1`),
		serverID, now, "minute", m.CPUPercent, m.MemoryPercent, m.DiskPercent,
		m.MemoryUsedBytes, m.MemoryTotalBytes, m.DiskUsedBytes, m.DiskTotalBytes,
		m.Load1, m.NetRxBytes, m.NetTxBytes)
	return err
}

type MetricPoint struct {
	ServerID         string    `db:"server_id" json:"server_id"`
	BucketAt         time.Time `db:"bucket_at" json:"bucket_at"`
	CPUPercent       float64   `db:"cpu_percent" json:"cpu_percent"`
	MemoryPercent    float64   `db:"memory_percent" json:"memory_percent"`
	DiskPercent      float64   `db:"disk_percent" json:"disk_percent"`
	MemoryUsedBytes  uint64    `db:"memory_used_bytes" json:"memory_used_bytes"`
	MemoryTotalBytes uint64    `db:"memory_total_bytes" json:"memory_total_bytes"`
	DiskUsedBytes    uint64    `db:"disk_used_bytes" json:"disk_used_bytes"`
	DiskTotalBytes   uint64    `db:"disk_total_bytes" json:"disk_total_bytes"`
	Load1            float64   `db:"load_1" json:"load_1"`
	NetRxBytes       uint64    `db:"net_rx_bytes" json:"net_rx_bytes"`
	NetTxBytes       uint64    `db:"net_tx_bytes" json:"net_tx_bytes"`
}

func (s *Store) Metrics(ctx context.Context, serverID string, since time.Time) ([]MetricPoint, error) {
	var out []MetricPoint
	err := s.DB.SelectContext(ctx, &out, s.Q(`SELECT server_id,bucket_at,cpu_percent,memory_percent,disk_percent,memory_used_bytes,memory_total_bytes,disk_used_bytes,disk_total_bytes,load_1,net_rx_bytes,net_tx_bytes FROM metric_rollups WHERE server_id=? AND bucket_at>=? ORDER BY bucket_at`), serverID, since)
	return out, err
}
