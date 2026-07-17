package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestUpdateServerMetadata(t *testing.T) {
	database := openBootstrapTestStore(t)
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "node-1", []string{"/srv"}, "update-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "update-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "node-1.local", "update-agent-token")
	if err != nil {
		t.Fatal(err)
	}

	api := &API{store: database}
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", server.ID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	response := directJSONRequest(t, http.MethodPatch, "/api/servers/"+server.ID, map[string]string{
		"address": "  server.example.com  ", "configuration": "  8 vCPU / 16 GB RAM  ", "notes": "  Primary API  ",
	}, nil, func(w http.ResponseWriter, r *http.Request) {
		api.updateServer(w, r.WithContext(requestContext))
	})
	if response.Code != http.StatusOK {
		t.Fatalf("metadata update returned %d: %s", response.Code, response.Body.String())
	}
	servers, err := database.ListServers(ctx)
	if err != nil || len(servers) != 1 || servers[0].Address != "server.example.com" || servers[0].Configuration != "8 vCPU / 16 GB RAM" || servers[0].Notes != "Primary API" {
		t.Fatalf("unexpected updated server: %#v %v", servers, err)
	}
}

func TestNormalizeServerMetadataRejectsOversizedFields(t *testing.T) {
	if _, err := normalizeServerMetadata("", "", strings.Repeat("备", serverNotesLimit)); err != nil {
		t.Fatalf("Unicode notes at the limit should be accepted: %v", err)
	}
	if _, err := normalizeServerMetadata("", "", strings.Repeat("备", serverNotesLimit+1)); err == nil {
		t.Fatal("expected oversized notes to be rejected")
	}
}

func TestDiscoverProjectsQueuesInventoryScan(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "discover-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := directJSONRequest(t, http.MethodPost, "/api/projects/discover", map[string]string{"server_id": server.ID}, &store.Session{UserID: "test-user"}, api.discoverProjects)
	if response.Code != http.StatusAccepted {
		t.Fatalf("project discovery returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(context.Background(), server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "inventory.scan" {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
}

func TestDiscoverProjectsRejectsMissingAndOfflineServers(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "offline-token")
	api := resourceTestAPI(database)
	for name, test := range map[string]struct {
		serverID string
		want     int
	}{
		"missing": {serverID: "missing", want: http.StatusNotFound},
		"offline": {serverID: server.ID, want: http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			response := directJSONRequest(t, http.MethodPost, "/api/projects/discover", map[string]string{"server_id": test.serverID}, &store.Session{UserID: "test-user"}, api.discoverProjects)
			if response.Code != test.want {
				t.Fatalf("returned %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func enrollResourceTestServer(t *testing.T, database *store.Store, token string) store.Server {
	t.Helper()
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "node-1", []string{"/srv"}, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "node-1.local", token+"-agent")
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func resourceTestAPI(database *store.Store) *API {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault := security.DevVault()
	hub := realtime.New()
	return &API{store: database, hub: hub, gateway: agentgateway.New(database, hub, vault, log), vault: vault, log: log}
}
