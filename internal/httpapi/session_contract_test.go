package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

// These contracts deliberately exercise the authenticated router. Differences
// here are compatibility requirements, not suggestions for the unified API.
type sessionContract struct {
	t                                         *testing.T
	db                                        *store.Store
	handler                                   http.Handler
	engine, prefix, id, workspaceID, serverID string
}

func newSessionContract(t *testing.T, engine string) *sessionContract {
	t.Helper()
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "contract-agent")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: buildinfo.Version, GeminiVersion: "0.66.0", ClaudeVersion: "2.1.286"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/contract", Name: "contract"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v %v", workspaces, err)
	}
	user := store.User{ID: "contract-user", Username: "contract-user", AuthMode: store.AuthModePassword}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user.ID, store.HashToken("contract-cookie"), "contract-csrf", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	prefix := "/api/" + engine + "/threads"
	if engine == "codex" {
		prefix = "/api/threads"
	}
	f := &sessionContract{t: t, db: db, engine: engine, prefix: prefix, workspaceID: workspaces[0].ID, serverID: server.ID, handler: New(db, a.hub, a.gateway, a.vault, a.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)}
	result := f.call("POST", prefix, fmt.Sprintf(`{"workspace_id":%q}`, f.workspaceID), 201)
	f.id, _ = result["id"].(string)
	if f.id == "" {
		t.Fatal(result)
	}
	for key, want := range map[string]any{"workspace_id": f.workspaceID, "status": "idle", "archived_at": nil, "pinned_at": nil} {
		if !reflect.DeepEqual(result[key], want) {
			t.Fatalf("create %s: %#v != %#v", key, result[key], want)
		}
	}
	defaults := map[string]string{"codex": "New session", "gemini": "Gemini", "claude": "Claude"}
	if result["title"] != defaults[engine] {
		t.Fatalf("create title: %v", result)
	}
	f.assertGolden("create", result)
	return f
}

func (f *sessionContract) request(method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "contract-cookie"})
	r.Header.Set("X-CSRF-Token", "contract-csrf")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}
func (f *sessionContract) call(method, path, body string, status int) map[string]any {
	f.t.Helper()
	w := f.request(method, path, body)
	if w.Code != status {
		f.t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), status)
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		f.t.Fatal(err)
	}
	return result
}
func (f *sessionContract) path(suffix string) string { return f.prefix + "/" + f.id + suffix }
func (f *sessionContract) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.DB.ExecContext(context.Background(), f.db.Q(query), args...); err != nil {
		f.t.Fatal(err)
	}
}
func (f *sessionContract) add(kind, payload string) protocol.StreamEvent {
	f.t.Helper()
	e, err := f.db.AddEvent(context.Background(), protocol.StreamEvent{StreamID: f.id, Kind: kind, Payload: json.RawMessage(payload)})
	if err != nil {
		f.t.Fatal(err)
	}
	return e
}
func (f *sessionContract) operations() []store.Operation {
	f.t.Helper()
	ops, err := f.db.PendingOperations(context.Background(), f.serverID)
	if err != nil {
		f.t.Fatal(err)
	}
	return ops
}
func (f *sessionContract) complete() {
	f.t.Helper()
	ctx := context.Background()
	for _, op := range f.operations() {
		if err := f.db.CompleteOperation(ctx, protocol.OperationResult{OperationID: op.ID, Status: "succeeded"}); err != nil {
			f.t.Fatal(err)
		}
		switch f.engine {
		case "codex":
			if err := f.db.SetThreadStatus(ctx, f.id, "idle"); err != nil {
				f.t.Fatal(err)
			}
		case "gemini":
			if err := f.db.CompleteGeminiTurn(ctx, f.id, op.ID, "idle"); err != nil {
				f.t.Fatal(err)
			}
		case "claude":
			f.exec("UPDATE claude_threads SET status='idle' WHERE id=? AND operation_id=?", f.id, op.ID)
		}
	}
}

func TestLegacySessionContracts(t *testing.T) {
	for _, engine := range []string{"codex", "gemini", "claude"} {
		t.Run(engine, func(t *testing.T) {
			cases := []struct {
				name string
				run  func(*testing.T, *sessionContract)
			}{
				{"create-detail-update-list", func(t *testing.T, f *sessionContract) {
					detail := f.call("GET", f.path(""), "", 200)
					f.assertGolden("detail", detail)
					if detail["id"] != f.id {
						t.Fatal(detail)
					}
					updated := f.call("PATCH", f.path(""), `{"title":"  renamed  ","pinned":true}`, 200)
					if engine == "codex" {
						if updated["title"] != "renamed" {
							t.Fatal(updated)
						}
					} else if !reflect.DeepEqual(updated, map[string]any{"ok": true}) {
						t.Fatal(updated)
					}
					detail = f.call("GET", f.path(""), "", 200)
					if detail["title"] != "renamed" || detail["pinned_at"] == nil {
						t.Fatal(detail)
					}
					w := f.request("GET", f.prefix, "")
					var list []map[string]any
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 || list[0]["id"] != f.id {
						t.Fatalf("list: %s", w.Body)
					}
					if engine == "codex" {
						page := f.call("GET", f.prefix+"?limit=1&offset=0", "", 200)
						if page["has_more"] != false || page["next"] != nil || len(page["items"].([]any)) != 1 {
							t.Fatal(page)
						}
						f.call("GET", f.prefix+"?archived=false", "", 400)
					} else {
						if w := f.request("GET", f.prefix+"?archived=false", ""); w.Code != 200 {
							t.Fatal(w.Body)
						}
					}
					emptyStatus := 200
					if engine == "codex" {
						emptyStatus = 400
					}
					f.call("PATCH", f.path(""), `{}`, emptyStatus)
					bad := f.call("PATCH", f.path(""), `{"title":" "}`, 400)
					want := "invalid session title"
					if engine == "codex" {
						want = "thread title is required"
					}
					if bad["error"] != want {
						t.Fatal(bad)
					}
				}},
				{"queue-lock-complete", func(t *testing.T, f *sessionContract) {
					response := f.call("POST", f.path("/turns"), `{"prompt":"first"}`, 202)
					if engine == "codex" {
						if response["operation_id"] == "" {
							t.Fatal(response)
						}
					} else if !reflect.DeepEqual(response, map[string]any{"ok": true}) {
						t.Fatal(response)
					}
					ops := f.operations()
					if len(ops) != 1 || ops[0].Kind != engine+".turn.start" || ops[0].WorkspaceWrite != map[string]int{"codex": 0, "gemini": 1, "claude": 1}[engine] {
						t.Fatal(ops)
					}
					if f.call("GET", f.path(""), "", 200)["status"] != "queued" {
						t.Fatal("turn not queued")
					}
					f.call("POST", f.path("/turns"), `{"prompt":"duplicate"}`, 409)
					f.call("PATCH", f.path(""), `{"archived":true}`, 409)
					f.call("DELETE", f.path(""), "", 409)
					f.complete()
					f.call("POST", f.path("/turns"), `{"prompt":"second"}`, 202)
				}},
				{"interrupt-queued", func(t *testing.T, f *sessionContract) {
					f.call("POST", f.path("/turns"), `{"prompt":"cancel"}`, 202)
					response := f.call("POST", f.path("/interrupt"), `{}`, 202)
					if engine == "codex" {
						if response["cancelled"] != true || response["operation_id"] == "" {
							t.Fatal(response)
						}
					} else if !reflect.DeepEqual(response, map[string]any{"ok": true}) {
						t.Fatal(response)
					}
					f.call("POST", f.path("/turns"), `{"prompt":"after cancellation"}`, 202)
				}},
				{"confirm-stopped", func(t *testing.T, f *sessionContract) {
					if engine == "codex" {
						t.Skip("Codex has no confirm-stopped route; exact turn cancellation is handled by interrupt")
					}
					response := f.call("POST", f.path("/confirm-stopped"), `{}`, 400)
					if response["error"] != "confirmation and operation ID are required" {
						t.Fatal(response)
					}
					f.call("POST", f.path("/confirm-stopped"), `{"confirmed":true,"operation_id":"stale"}`, 409)
					f.call("POST", f.path("/turns"), `{"prompt":"running"}`, 202)
					ops := f.operations()
					if len(ops) != 1 {
						t.Fatal(ops)
					}
					f.exec("UPDATE agent_operations SET status='delivered' WHERE id=?", ops[0].ID)
					f.exec("UPDATE "+engine+"_threads SET status='interrupted' WHERE id=?", f.id)
					f.call("POST", f.path("/confirm-stopped"), fmt.Sprintf(`{"confirmed":true,"operation_id":%q}`, ops[0].ID), 200)
					op, err := f.db.Operation(context.Background(), ops[0].ID)
					if err != nil || op.Status != "cancelled" {
						t.Fatalf("confirmation: %+v %v", op, err)
					}
					f.call("POST", f.path("/turns"), `{"prompt":"after confirmed exit"}`, 202)

				}},
				{"archive-delete", func(t *testing.T, f *sessionContract) {
					f.call("PATCH", f.path(""), `{"archived":true}`, 200)
					if f.call("GET", f.path(""), "", 200)["archived_at"] == nil {
						t.Fatal("archive lost")
					}
					f.call("POST", f.path("/turns"), `{"prompt":"blocked"}`, 409)
					w := f.request("GET", f.prefix+"?archived=true", "")
					var items []map[string]any
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != 1 {
						t.Fatalf("archived list: %s", w.Body)
					}
					w = f.request("GET", f.prefix+"?archived=all", "")
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &items) != nil {
						t.Fatal(w.Body)
					}
					want := 0
					if engine == "codex" {
						want = 1
					}
					if len(items) != want {
						t.Fatalf("archived all: %s", w.Body)
					}
					f.call("PATCH", f.path(""), `{"archived":false}`, 200)
					if response := f.call("DELETE", f.path(""), "", 200); !reflect.DeepEqual(response, map[string]any{"ok": true}) {
						t.Fatal(response)
					}
					response := f.call("GET", f.path(""), "", 404)
					name := map[string]string{"codex": "Codex", "gemini": "Gemini", "claude": "Claude"}[engine]
					if response["error"] != name+" session not found" {
						t.Fatal(response)
					}
				}},
				{"events-usage-validation", func(t *testing.T, f *sessionContract) {
					kind := engine + ".user.message"
					if engine == "codex" {
						kind = "user.message"
					}
					first := f.add(kind, `{"text":"one"}`)
					last := f.add(kind, `{"text":"two"}`)
					for _, q := range []string{"?after=0", "?before=" + fmt.Sprint(last.Sequence+1), ""} {
						w := f.request("GET", f.path("/events")+q, "")
						var events []protocol.StreamEvent
						if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &events) != nil {
							t.Fatal(w.Body)
						}
						seen := false
						for _, e := range events {
							if e.EventID == first.EventID {
								seen = true
							}
						}
						if !seen {
							t.Fatalf("missing event %s: %s", q, w.Body)
						}
					}
					f.call("GET", f.path("/events?after=0&before=1"), "", 400)
					invalid := f.call("GET", f.path("/events?after=-1"), "", 400)
					want := "invalid event cursor"
					if engine == "codex" {
						want = "after must be a non-negative sequence number"
					}
					if invalid["error"] != want {
						t.Fatal(invalid)
					}
					usage := f.call("GET", f.path("/usage"), "", 200)
					expected := map[string]any{"input_tokens": nil, "output_tokens": nil, "per_minute": nil, "output_per_second": nil, "partial": false}
					if !reflect.DeepEqual(usage, expected) {
						t.Fatal(usage)
					}
				}},
				{"rewrite-invalid-boundary", func(t *testing.T, f *sessionContract) {
					status := 409
					if engine == "codex" {
						status = 400
					}
					f.call("POST", f.path("/events/missing/rewrite"), `{"prompt":"replacement"}`, status)
					f.call("POST", f.path("/turns"), `{}`, 400)
				}},
				{"rewrite-completed-turn", func(t *testing.T, f *sessionContract) {
					f.call("POST", f.path("/turns"), `{"prompt":"original"}`, 202)
					ops := f.operations()
					if len(ops) != 1 {
						t.Fatal(ops)
					}
					column := "session_id"
					if engine == "codex" {
						column = "codex_thread_id"
					}
					f.exec("UPDATE "+engine+"_threads SET "+column+"='native-original' WHERE id=?", f.id)
					if engine == "gemini" {
						f.add("gemini.init", fmt.Sprintf(`{"operation_id":%q,"session_id":"native-original","timestamp":"2026-11-15T00:00:00Z"}`, ops[0].ID))
					}
					events, err := f.db.Events(context.Background(), f.id, 0, 500)
					if err != nil {
						t.Fatal(err)
					}
					target := ""
					for _, e := range events {
						if e.Kind == engine+".user.message" || (engine == "codex" && e.Kind == "user.message") {
							target = e.EventID
						}
					}
					if target == "" {
						t.Fatalf("no user event: %+v", events)
					}
					f.complete()
					response := f.call("POST", f.path("/events/"+target+"/rewrite"), `{"prompt":"replacement"}`, 202)
					if engine == "codex" {
						if response["operation_id"] == nil {
							t.Fatal(response)
						}
					} else if !reflect.DeepEqual(response, map[string]any{"ok": true}) {
						t.Fatal(response)
					}
					ops = f.operations()
					if len(ops) != 1 {
						t.Fatal(ops)
					}
					if engine != "codex" && !strings.Contains(ops[0].Payload, `"rewrite"`) {
						t.Fatal(ops)
					}
				}},
				{"steer-running", func(t *testing.T, f *sessionContract) {
					f.call("POST", f.path("/turns"), `{"prompt":"start"}`, 202)
					ops := f.operations()
					if len(ops) != 1 {
						t.Fatal(ops)
					}
					op := ops[0].ID
					f.exec("UPDATE agent_operations SET status='delivered' WHERE id=?", op)
					column := "session_id"
					if engine == "codex" {
						column = "codex_thread_id"
					}
					f.exec("UPDATE "+engine+"_threads SET status='running',"+column+"='native-original' WHERE id=?", f.id)
					switch engine {
					case "codex":
						f.add("codex.turn.started", `{"turn":{"id":"native-turn"}}`)
					case "gemini":
						f.add("gemini.init", fmt.Sprintf(`{"operation_id":%q,"session_id":"native-original"}`, op))
						f.add("gemini.steer.ready", fmt.Sprintf(`{"operation_id":%q}`, op))
					case "claude":
						f.add("claude.steer.ready", fmt.Sprintf(`{"operation_id":%q,"ready":true}`, op))
					}
					body := fmt.Sprintf(`{"prompt":"focus","operation_id":%q,"request_id":%q}`, op, store.NewID())
					if engine == "codex" {
						body = fmt.Sprintf(`{"prompt":"focus","request_id":%q}`, store.NewID())
					}
					response := f.call("POST", f.path("/steer"), body, 202)
					if response["operation_id"] == nil {
						t.Fatal(response)
					}
					for _, item := range f.operations() {
						if item.Kind == engine+".turn.steer" && item.WorkspaceWrite != 0 {
							t.Fatal(item)
						}
					}
				}},
				{"steer-validation", func(t *testing.T, f *sessionContract) { f.call("POST", f.path("/steer"), `{}`, 400) }},
				{"fork", func(t *testing.T, f *sessionContract) {
					f.call("POST", f.path("/fork"), `{}`, 409)
					column := "session_id"
					if engine == "codex" {
						column = "codex_thread_id"
					}
					f.exec("UPDATE "+engine+"_threads SET "+column+"='native-source' WHERE id=?", f.id)
					response := f.call("POST", f.path("/fork"), `{}`, 202)
					if response["target_thread_id"] == nil || response["target_thread_id"] == f.id {
						t.Fatal(response)
					}
					ops := f.operations()
					if len(ops) != 1 || ops[0].Kind != engine+".thread.fork" {
						t.Fatal(ops)
					}
					want := 1
					if engine == "codex" {
						want = 0
					}
					if ops[0].WorkspaceWrite != want {
						t.Fatal(ops)
					}
				}},
				{"fork-worktree", func(t *testing.T, f *sessionContract) {
					f.call("POST", f.path("/fork-worktree"), `{}`, 409)
					column := "session_id"
					if engine == "codex" {
						column = "codex_thread_id"
					}
					f.exec("UPDATE "+engine+"_threads SET "+column+"='native-source' WHERE id=?", f.id)
					response := f.call("POST", f.path("/fork-worktree"), `{"branch":"contract-branch","path":"/srv/contract-branch"}`, 202)
					if response["operation_id"] == nil || response["target_thread_id"] == nil {
						t.Fatal(response)
					}
					ops := f.operations()
					if len(ops) != 1 || ops[0].Kind != "git.worktree.create" || ops[0].WorkspaceWrite != 1 {
						t.Fatal(ops)
					}
				}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) { f := newSessionContract(t, engine); tc.run(t, f) })
			}
		})
	}
}

// Golden files cover every existing response field. The additive capabilities
// object is introduced by P2-02 and has its own version-boundary contracts.
func (f *sessionContract) assertGolden(name string, value map[string]any) {
	f.t.Helper()
	normalized := map[string]any{}
	for key, v := range value {
		if key == "capabilities" {
			continue
		}
		if text, ok := v.(string); ok && text != "" {
			if strings.HasSuffix(key, "_at") {
				v = "<time>"
			} else if key == "id" || strings.HasSuffix(key, "_id") {
				v = "<id>"
			}
		}
		normalized[key] = v
	}
	raw, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join("testdata", "sessions", f.engine+"-"+name+".json")
	if os.Getenv("UPDATE_SESSION_CONTRACTS") == "1" {
		if err := os.WriteFile(path, append(raw, '\n'), 0644); err != nil {
			f.t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != strings.TrimSpace(string(expected)) {
		f.t.Fatalf("%s response changed:\n%s\nwant:\n%s", path, raw, expected)
	}
}
