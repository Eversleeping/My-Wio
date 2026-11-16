package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessionservice"
	"github.com/wio-platform/wio/internal/sessiontools"
)

func TestDeclaredCapabilitiesReachHTTPAndEnforceMinimum(t *testing.T) {
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		t.Run(engine, func(t *testing.T) {
			f := newSessionContract(t, engine)
			all := capabilities.AgentDeclaration("0.66.0", "2.1.286")
			cases := []struct {
				name, version, gemini, claude string
				declared                      capabilities.Declaration
				supported                     bool
			}{
				{"absent", "9.9.9", "0.66.0", "2.1.286", nil, false},
				{"declared_empty", "9.9.9", "0.66.0", "2.1.286", capabilities.Declaration{}, false},
				{"unknown_only", "9.9.9", "0.66.0", "2.1.286", capabilities.Declaration{"future.unknown"}, false},
				{"authoritative", "0.3.26", "invalid", "invalid", all, true},
				{"below_minimum", "0.3.25", "0.66.0", "2.1.286", all, false},
				{"declaration_removed", "9.9.9", "0.66.0", "2.1.286", nil, false},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					if err := f.db.Heartbeat(context.Background(), f.serverID, protocol.Heartbeat{AgentVersion: tc.version, GeminiVersion: tc.gemini, ClaudeVersion: tc.claude, Capabilities: tc.declared}); err != nil {
						t.Fatal(err)
					}
					cli := tc.gemini
					if engine == session.Claude {
						cli = tc.claude
					}
					want := session.ResolveCapabilities(engine, tc.version, cli, tc.declared)
					for _, path := range []string{f.path(""), fmt.Sprintf("/api/sessions/%s/%s", engine, f.id)} {
						response := f.call(http.MethodGet, path, "", http.StatusOK)
						raw, _ := json.Marshal(response["capabilities"])
						var got session.Capabilities
						if err := json.Unmarshal(raw, &got); err != nil || got != want {
							t.Fatalf("%s: capabilities=%+v want=%+v %v", path, got, want, err)
						}
						if _, leaked := response["declared_capabilities"]; leaked {
							t.Fatal("internal declaration leaked")
						}
						for key, value := range response {
							if strings.HasSuffix(key, "_supported") && value != tc.supported {
								t.Fatalf("%s %s=%v want %v", path, key, value, tc.supported)
							}
						}
					}
					serverResponse := f.request(http.MethodGet, "/api/servers", "")
					if serverResponse.Code != http.StatusOK {
						t.Fatal(serverResponse.Body.String())
					}
					var servers []map[string]any
					if err := json.Unmarshal(serverResponse.Body.Bytes(), &servers); err != nil {
						t.Fatal(err)
					}
					found := false
					for _, server := range servers {
						if server["id"] == f.serverID {
							found = true
							for _, key := range []string{"agent_update_supported", "codex_update_supported", "cli_management_supported", "exact_rollback_supported", "scoped_credentials_supported", "gemini_credentials_supported", "claude_effort_supported", "claude_credentials_supported"} {
								want := tc.supported || (tc.name == "below_minimum" && key == "agent_update_supported")
								if server[key] != want {
									t.Fatalf("server %s=%v want %v", key, server[key], want)
								}
							}
							if _, leaked := server["declared_capabilities"]; leaked {
								t.Fatal("internal server declaration leaked")
							}
						}
					}
					if !found {
						t.Fatal("missing server")
					}

					availabilityResponse := f.request(http.MethodGet, "/api/workspaces/"+f.workspaceID+"/session-engines", "")
					var availability sessionservice.WorkspaceEngines
					if availabilityResponse.Code != http.StatusOK {
						t.Fatal(availabilityResponse.Body.String())
					}
					if err := json.Unmarshal(availabilityResponse.Body.Bytes(), &availability); err != nil {
						t.Fatal(err)
					}
					foundEngine := false
					for _, item := range availability.Engines {
						if item.Engine == engine {
							foundEngine = true
							if item.Capabilities != want {
								t.Fatalf("workspace availability capabilities=%+v want %+v", item.Capabilities, want)
							}
						}
					}
					if !foundEngine {
						t.Fatal("missing workspace engine")
					}
					collaboration, err := f.db.ReadCollaborationState(context.Background(), sessiontools.Ref{Engine: engine, ID: f.id})
					if err != nil || collaboration["supported"] != tc.supported {
						t.Fatalf("collaboration support=%v want %v: %v", collaboration["supported"], tc.supported, err)
					}
					items, err := f.db.AISessions(context.Background(), session.Filter{Engine: engine})
					if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0].Capabilities, want) {
						t.Fatalf("session list: %+v %v", items, err)
					}
					if !tc.supported && engine != session.Codex {
						response := f.request(http.MethodPost, fmt.Sprintf("/api/sessions/%s/%s/turns", engine, f.id), `{"prompt":"must not dispatch"}`)
						if response.Code != http.StatusConflict {
							t.Fatalf("undeclared turn accepted: %d %s", response.Code, response.Body.String())
						}
						operations, err := f.db.PendingOperations(context.Background(), f.serverID)
						if err != nil || len(operations) != 0 {
							t.Fatalf("unsupported turn queued: %+v %v", operations, err)
						}
					}
				})
			}
		})
	}
}
