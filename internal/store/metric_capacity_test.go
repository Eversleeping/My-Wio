package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestOpenMigratesLegacyMetricCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE metric_rollups (
		server_id TEXT NOT NULL,bucket_at TIMESTAMP NOT NULL,resolution TEXT NOT NULL DEFAULT 'minute',
		cpu_percent REAL NOT NULL,memory_percent REAL NOT NULL,disk_percent REAL NOT NULL,
		load_1 REAL NOT NULL,net_rx_bytes BIGINT NOT NULL,net_tx_bytes BIGINT NOT NULL,
		samples INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(server_id,bucket_at,resolution));
		INSERT INTO metric_rollups(server_id,bucket_at,cpu_percent,memory_percent,disk_percent,load_1,net_rx_bytes,net_tx_bytes)
		VALUES('legacy',CURRENT_TIMESTAMP,10,43.4,19.4,1,0,0)`)
	_ = legacy.Close()
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		database, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		points, err := database.Metrics(context.Background(), "legacy", time.Now().UTC().Add(-time.Hour))
		_ = database.Close()
		if err != nil || len(points) != 1 {
			t.Fatalf("legacy metric not preserved: %#v %v", points, err)
		}
		if points[0].MemoryPercent != 43.4 || points[0].DiskPercent != 19.4 || points[0].MemoryTotalBytes != 0 || points[0].DiskTotalBytes != 0 {
			t.Fatalf("legacy metric changed or capacity fabricated: %#v", points[0])
		}
	}
}

func TestMetricCapacityStorage(t *testing.T) {
	database := testStore(t)
	server := enrollProjectImportTestServer(t, database, "capacity")
	assertMetricCapacityStorage(t, database, server.ID)
}

func assertMetricCapacityStorage(t *testing.T, database *Store, serverID string) {
	t.Helper()
	ctx := context.Background()
	const gib = uint64(1 << 30)
	metric := protocol.Metrics{MemoryPercent: 50, DiskPercent: 25,
		MemoryUsedBytes: 4 * gib, MemoryTotalBytes: 8 * gib,
		DiskUsedBytes: 1024 * gib, DiskTotalBytes: 4096 * gib}
	if err := database.SaveMetrics(ctx, serverID, metric); err != nil {
		t.Fatal(err)
	}
	metric.MemoryUsedBytes = 0 // A genuinely empty resource must stay distinguishable from missing data.
	metric.DiskUsedBytes = 1025 * gib
	if err := database.SaveMetrics(ctx, serverID, metric); err != nil {
		t.Fatal(err)
	}
	// An older Agent or failed collector cannot overwrite known byte counts with missing fields.
	if err := database.SaveMetrics(ctx, serverID, protocol.Metrics{MemoryPercent: 20, DiskPercent: 30}); err != nil {
		t.Fatal(err)
	}
	points, err := database.Metrics(ctx, serverID, time.Now().UTC().Add(-time.Hour))
	if err != nil || len(points) == 0 {
		t.Fatalf("list capacity metrics: %#v %v", points, err)
	}
	latest := points[len(points)-1]
	if latest.MemoryUsedBytes != 0 || latest.MemoryTotalBytes != metric.MemoryTotalBytes || latest.DiskUsedBytes != metric.DiskUsedBytes || latest.DiskTotalBytes != metric.DiskTotalBytes {
		t.Fatalf("capacity did not retain latest valid 64-bit counters: %#v", latest)
	}
}
