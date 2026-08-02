package store

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestOperationMetricsAggregatesLatencyAndStatuses(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	server := enrollProjectImportTestServer(t, database, "metrics-server")
	now := time.Now().UTC().Truncate(time.Millisecond)

	type operationFixture struct {
		id        string
		status    string
		created   time.Time
		delivered *time.Time
		started   *time.Time
		complete  *time.Time
	}
	makeTime := func(offset time.Duration) *time.Time {
		value := now.Add(offset)
		return &value
	}
	fixtures := []operationFixture{
		{id: "metrics-succeeded", status: "succeeded", created: now.Add(-100 * time.Second), delivered: makeTime(-90 * time.Second), started: makeTime(-80 * time.Second), complete: makeTime(-50 * time.Second)},
		{id: "metrics-failed", status: "failed", created: now.Add(-70 * time.Second), delivered: makeTime(-65 * time.Second), complete: makeTime(-60 * time.Second)},
		{id: "metrics-running", status: "running", created: now.Add(-40 * time.Second), delivered: makeTime(-35 * time.Second), started: makeTime(-34 * time.Second)},
		{id: "metrics-queued", status: "queued", created: now.Add(-30 * time.Second)},
		{id: "metrics-cancelled", status: "cancelled", created: now.Add(-20 * time.Second)},
		{id: "metrics-old", status: "succeeded", created: now.Add(-3 * time.Hour), delivered: makeTime(-3*time.Hour + 10*time.Second), started: makeTime(-3*time.Hour + 20*time.Second), complete: makeTime(-3*time.Hour + 30*time.Second)},
	}
	for _, fixture := range fixtures {
		if _, err := database.DB.ExecContext(ctx, database.Q(`INSERT INTO agent_operations(id,server_id,kind,idempotency_key,status,created_at,delivered_at,started_at,completed_at) VALUES(?,?,?, ?,?,?,?,?,?)`), fixture.id, server.ID, "metrics.test", fixture.id, fixture.status, fixture.created, fixture.delivered, fixture.started, fixture.complete); err != nil {
			t.Fatalf("insert %s: %v", fixture.id, err)
		}
	}

	metrics, err := database.OperationMetrics(ctx, server.ID, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Total != 5 || metrics.Queued != 1 || metrics.Delivered != 0 || metrics.Running != 1 || metrics.Succeeded != 1 || metrics.Failed != 1 || metrics.Cancelled != 1 {
		t.Fatalf("unexpected status totals: %#v", metrics)
	}
	if metrics.QueueWait.Count != 3 || math.Abs(metrics.QueueWait.Average-6666.666666666667) > 0.001 || metrics.QueueWait.P95 != 10000 || metrics.QueueWait.Max != 10000 {
		t.Fatalf("unexpected queue wait aggregate: %#v", metrics.QueueWait)
	}
	if metrics.Delivery.Count != 3 || math.Abs(metrics.Delivery.Average-3666.6666666666665) > 0.001 || metrics.Delivery.P95 != 10000 || metrics.Delivery.Max != 10000 {
		t.Fatalf("unexpected delivery aggregate: %#v", metrics.Delivery)
	}
	if metrics.Execution.Count != 2 || metrics.Execution.Average != 17500 || metrics.Execution.P95 != 30000 || metrics.Execution.Max != 30000 {
		t.Fatalf("unexpected execution aggregate: %#v", metrics.Execution)
	}
}

func TestMarkRunningRecordsStartOnce(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	server := enrollProjectImportTestServer(t, database, "running-server")
	operationID, err := database.QueueOperation(ctx, server.ID, "metrics.test", map[string]any{}, "running-operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkDelivered(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	first, err := database.Operation(ctx, operationID)
	if err != nil || first.Status != "running" || first.StartedAt == nil {
		t.Fatalf("operation was not marked running: %#v %v", first, err)
	}
	startedAt := *first.StartedAt
	if err := database.MarkRunning(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	second, err := database.Operation(ctx, operationID)
	if err != nil || second.StartedAt == nil || !second.StartedAt.Equal(startedAt) {
		t.Fatalf("operation start timestamp changed on retry: %#v %v", second, err)
	}
}
