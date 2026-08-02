package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestOperationMetricsEndpointReturnsStableContract(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "operation-metrics-token")
	operationID, err := database.QueueOperation(context.Background(), server.ID, "metrics.test", map[string]any{}, "operation-metrics-contract")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkDelivered(context.Background(), operationID); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(context.Background(), operationID); err != nil {
		t.Fatal(err)
	}

	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", server.ID)
	request := httptest.NewRequest(http.MethodGet, "/api/servers/"+server.ID+"/operation-metrics?hours=2", nil)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	resourceTestAPI(database).operationMetrics(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("operation metrics returned %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		ServerID  string    `json:"server_id"`
		Since     time.Time `json:"since"`
		Total     int       `json:"total"`
		Running   int       `json:"running"`
		QueueWait struct {
			Count int `json:"count"`
		} `json:"queue_wait"`
		Delivery struct {
			Count int `json:"count"`
		} `json:"delivery"`
		Execution struct {
			Count int `json:"count"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ServerID != server.ID || payload.Total != 1 || payload.Running != 1 || payload.QueueWait.Count != 1 || payload.Delivery.Count != 1 || payload.Execution.Count != 0 {
		t.Fatalf("unexpected operation metrics contract: %#v", payload)
	}
	if payload.Since.Before(time.Now().UTC().Add(-2*time.Hour-time.Minute)) || payload.Since.After(time.Now().UTC().Add(-2*time.Hour+time.Minute)) {
		t.Fatalf("unexpected since window: %s", payload.Since)
	}
}

func TestOperationMetricsEndpointClampsInvalidHours(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "operation-metrics-hours-token")
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", server.ID)
	request := httptest.NewRequest(http.MethodGet, "/api/servers/"+server.ID+"/operation-metrics?hours=9999", nil)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	resourceTestAPI(database).operationMetrics(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("operation metrics with invalid hours returned %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Since time.Time `json:"since"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Since.Before(time.Now().UTC().Add(-24*time.Hour-time.Minute)) || payload.Since.After(time.Now().UTC().Add(-24*time.Hour+time.Minute)) {
		t.Fatalf("invalid hours should clamp to 24: %s", payload.Since)
	}
}
