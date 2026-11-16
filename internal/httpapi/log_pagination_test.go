package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLogEndpointsSupportOffsetAndKeepArrayResponses(t *testing.T) {
	database := openBootstrapTestStore(t)
	if _, err := database.DB.Exec("INSERT INTO servers(id,name) VALUES('log-node','Log node')"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("log-%d", i)
		status, alertStatus := "succeeded", "open"
		if i == 0 {
			status = "running"
		}
		if i == 2 {
			alertStatus = "resolved"
		}
		if _, err := database.DB.Exec(database.Q("INSERT INTO agent_operations(id,server_id,kind,status,idempotency_key,created_at) VALUES(?,?,?,?,?,?)"), id, "log-node", "inventory.scan", status, id, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := database.DB.Exec(database.Q("INSERT INTO alerts(id,kind,severity,title,status,opened_at) VALUES(?,?,?,?,?,?)"), id, "test", "warning", id, alertStatus, stamp); err != nil {
			t.Fatal(err)
		}
	}
	api := resourceTestAPI(database)
	for _, item := range []struct {
		path, first string
		count       int
	}{
		{"/api/operations", "log-0", 3},
		{"/api/operations?limit=1&offset=1", "log-2", 1},
		{"/api/operations?status=succeeded&limit=1&offset=1", "log-1", 1},
		{"/api/alerts", "log-1", 3},
		{"/api/alerts?limit=1&offset=1", "log-0", 1},
	} {
		handler := api.operations
		if strings.HasPrefix(item.path, "/api/alerts") {
			handler = api.alerts
		}
		response := directJSONRequest(t, http.MethodGet, item.path, nil, nil, handler)
		var rows []map[string]any
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &rows) != nil || len(rows) != item.count || rows[0]["id"] != item.first {
			t.Fatalf("%s: %d %s", item.path, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{"/api/operations", "/api/alerts"} {
		handler := api.operations
		if path == "/api/alerts" {
			handler = api.alerts
		}
		for _, query := range []string{"offset=-1", "offset=", "offset=abc", "offset=1&offset=2", "offset=999999999999999999999999"} {
			response := directJSONRequest(t, http.MethodGet, path+"?"+query, nil, nil, handler)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid %s?%s: %d", path, query, response.Code)
			}
		}
		response := directJSONRequest(t, http.MethodGet, path+"?limit=50&offset=500", nil, nil, handler)
		var rows []map[string]any
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &rows) != nil || len(rows) != 0 {
			t.Fatalf("empty last page: %d %s", response.Code, response.Body.String())
		}
	}
}
