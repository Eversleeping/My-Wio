package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestLogPagination(t *testing.T) { assertLogPagination(t, testStore(t)) }

func assertLogPagination(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, database, "log-page-node", "log-page-token")
	tx, err := database.DB.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 205; i++ {
		status, alertStatus := "succeeded", "open"
		if i == 0 {
			status, alertStatus = "running", "resolved"
		}
		id := fmt.Sprintf("log-op-%03d", i)
		if _, err = tx.ExecContext(ctx, database.Q("INSERT INTO agent_operations(id,server_id,kind,status,idempotency_key,created_at) VALUES(?,?,?,?,?,?)"), id, server.ID, "inventory.scan", status, id, stamp); err != nil {
			t.Fatal(err)
		}
		id = fmt.Sprintf("log-alert-%03d", i)
		if _, err = tx.ExecContext(ctx, database.Q("INSERT INTO alerts(id,server_id,kind,severity,title,status,opened_at) VALUES(?,?,?,?,?,?,?)"), id, server.ID, "test", "warning", id, alertStatus, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	operations, err := database.ListOperations(ctx, "", 500)
	if err != nil || len(operations) != 205 || operations[0].ID != "log-op-000" {
		t.Fatalf("active-first order: %d %v", len(operations), err)
	}
	alerts, err := database.ListAlertsPage(ctx, 500, 0)
	if err != nil || len(alerts) != 205 || alerts[0].ID != "log-alert-204" || alerts[204].Status != "resolved" {
		t.Fatalf("alert order: %d %v", len(alerts), err)
	}
	for offset := 0; offset <= 250; offset += 50 {
		ops, err := database.ListOperationsPage(ctx, "", 50, offset)
		if err != nil {
			t.Fatal(err)
		}
		page, err := database.ListAlertsPage(ctx, 50, offset)
		if err != nil {
			t.Fatal(err)
		}
		expected := min(50, max(0, 205-offset))
		if len(ops) != expected || len(page) != expected {
			t.Fatalf("offset %d: operations=%d alerts=%d expected=%d", offset, len(ops), len(page), expected)
		}
		for i := range ops {
			if ops[i].ID != operations[offset+i].ID || page[i].ID != alerts[offset+i].ID {
				t.Fatalf("unstable order at %d", offset+i)
			}
		}
	}
	filtered, err := database.ListOperationsPage(ctx, "succeeded", 50, 200)
	if err != nil || len(filtered) != 4 || filtered[0].ID != "log-op-004" {
		t.Fatalf("filter must apply before offset: %#v %v", filtered, err)
	}
	legacy, err := database.ListAlerts(ctx)
	if err != nil || len(legacy) != 200 {
		t.Fatalf("legacy alert bound: %d %v", len(legacy), err)
	}
}
