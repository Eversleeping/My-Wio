package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

// Exercise all native lifecycle scenarios through the unified public endpoints.
func TestUnifiedSessionContracts(t *testing.T) {
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		t.Run(engine, func(t *testing.T) {
			for _, tc := range sessionContractCases(engine) {
				t.Run(tc.name, func(t *testing.T) {
					f := newSessionContract(t, engine)
					tc.run(t, f)
				})
			}
		})
	}
}

func TestUnifiedSessionCRUD(t *testing.T) {
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		t.Run(engine, func(t *testing.T) {
			f := newSessionContract(t, engine)
			created := f.call("POST", f.prefix, fmt.Sprintf(`{"workspace_id":%q}`, f.workspaceID), 201)
			if created["engine"] != engine || created["mode"] == nil || created["workspace_name"] == nil {
				t.Fatal(created)
			}
			f.id = created["id"].(string)
			detail := f.call("GET", f.path(""), "", 200)
			if !reflect.DeepEqual(created, detail) {
				t.Fatalf("create/detail mismatch: %v %v", created, detail)
			}
			updated := f.call("PATCH", f.path(""), `{"title":"renamed","pinned":true}`, 200)
			if updated["title"] != "renamed" || updated["pinned_at"] == nil || updated["engine"] != engine {
				t.Fatal(updated)
			}
			for _, key := range []string{"project_name", "server_name", "server_status", "project_pinned_at", "project_hidden_at", "capabilities", "native_id", "mode", "workspace_name"} {
				if _, ok := updated[key]; !ok {
					t.Fatalf("missing %s: %v", key, updated)
				}
			}
			if engine != session.Codex {
				if _, legacy := updated["steer_supported"]; legacy {
					t.Fatal(updated)
				}
			}
			f.call("PATCH", f.path(""), `{"archived":true}`, 200)
			page := f.call("GET", "/api/sessions?engine="+engine+"&archived=true", "", 200)
			if len(page["items"].([]any)) != 1 {
				t.Fatal(page)
			}
			if len(f.call("GET", "/api/sessions?engine="+engine+"&archived=all", "", 200)["items"].([]any)) != 2 {
				t.Fatal("all view lost archive")
			}
			f.call("POST", f.path("/turns"), `{"prompt":"blocked"}`, 409)
			f.call("PATCH", f.path(""), `{"archived":false}`, 200)
			f.call("DELETE", f.path(""), "", 200)
			if f.call("GET", f.path(""), "", 404)["error"] != "session not found" {
				t.Fatal("missing session contract")
			}
		})
	}
}

func TestUnifiedSessionMixedListAndSearch(t *testing.T) {
	f := newSessionContract(t, session.Codex)
	ctx := context.Background()
	g, e := f.db.CreateGeminiThread(ctx, f.workspaceID, "auto", "default")
	if e != nil {
		t.Fatal(e)
	}
	c, e := f.db.CreateClaudeThread(ctx, f.workspaceID, "sonnet", "default", "")
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 11, 15, 1, 2, 3, 123456000, time.UTC)
	f.exec("UPDATE workspaces SET display_name='search-group',path='/srv/search-path' WHERE id=?", f.workspaceID)
	f.exec("UPDATE servers SET name='search-server' WHERE id=?", f.serverID)
	f.exec("UPDATE projects SET name='search-project' WHERE id=(SELECT project_id FROM workspaces WHERE id=?)", f.workspaceID)
	refs := []session.Ref{{Engine: session.Codex, ID: f.id}, {Engine: session.Gemini, ID: g.ID}, {Engine: session.Claude, ID: c.ID}}
	for _, ref := range refs {
		nativeColumn := "session_id"
		if ref.Engine == session.Codex {
			nativeColumn = "codex_thread_id"
		}
		f.exec("UPDATE "+ref.Engine+"_threads SET title=?,"+nativeColumn+"=?,updated_at=? WHERE id=?", "search-title-"+ref.Engine, "search-native-"+ref.Engine, at, ref.ID)
	}
	f.exec("UPDATE projects SET pinned_at=?,hidden_at=? WHERE id=(SELECT project_id FROM workspaces WHERE id=?)", at, at, f.workspaceID)
	f.exec("UPDATE codex_threads SET pinned_at=?,updated_at=? WHERE id=?", at, at.Add(-time.Hour), f.id)
	first := f.call("GET", "/api/sessions?limit=1", "", 200)
	head := first["items"].([]any)[0].(map[string]any)
	if head["engine"] != session.Codex || head["pinned_at"] == nil || head["project_pinned_at"] == nil || head["project_hidden_at"] == nil {
		t.Fatalf("common pins/group fields: %v", head)
	}
	if len(first) != 3 || len(first["items"].([]any)) != 1 || first["next_cursor"] == nil {
		t.Fatal(first)
	}
	groups := first["groups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["matched_count"] != float64(3) {
		t.Fatal(groups)
	}
	seen := map[string]bool{}
	page := first
	for i := 0; i < 4; i++ {
		if !reflect.DeepEqual(page["groups"], first["groups"]) {
			t.Fatal("truncated groups")
		}
		for _, raw := range page["items"].([]any) {
			item := raw.(map[string]any)
			key := item["engine"].(string) + ":" + item["id"].(string)
			if seen[key] {
				t.Fatal("duplicate page", key)
			}
			seen[key] = true
		}
		if page["next_cursor"] == nil {
			break
		}
		page = f.call("GET", "/api/sessions?limit=1&cursor="+url.QueryEscape(page["next_cursor"].(string)), "", 200)
	}
	if len(seen) != 3 {
		t.Fatal(seen)
	}
	for _, ref := range refs {
		// Six public search categories, including both IDs, before the limit.
		for _, query := range []string{"search-group", "search-project", "search-path", "search-title-" + ref.Engine, "search-server", ref.ID, "search-native-" + ref.Engine} {
			result := f.call("GET", "/api/sessions?engine="+ref.Engine+"&limit=1&q="+url.QueryEscape(query), "", 200)
			items := result["items"].([]any)
			if len(items) != 1 || items[0].(map[string]any)["id"] != ref.ID || result["next_cursor"] != nil {
				t.Fatalf("search %s %s: %v", ref.Engine, query, result)
			}
		}
	}
	for _, query := range []string{"?engine=unknown", "?archived=no", "?limit=0", "?limit=-1", "?limit=1&limit=2", "?offset=0", "?cursor=broken"} {
		f.call("GET", "/api/sessions"+query, "", 400)
	}
	f.call("GET", "/api/sessions?engine=claude&cursor="+url.QueryEscape(first["next_cursor"].(string)), "", 400)
	empty := f.call("GET", "/api/sessions?q=missing", "", 200)
	if len(empty["items"].([]any)) != 0 || len(empty["groups"].([]any)) != 0 || empty["next_cursor"] != nil {
		t.Fatal(empty)
	}
}

func TestUnifiedSessionEventWindowAndUnsupported(t *testing.T) {
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		t.Run(engine, func(t *testing.T) {
			f := newSessionContract(t, engine)
			for n := 0; n < 505; n++ {
				f.add("user.message", fmt.Sprintf(`{"text":"message %d"}`, n))
			}
			check := func(query string, want int) []protocol.StreamEvent {
				t.Helper()
				w := f.request("GET", f.path("/events")+query, "")
				var events []protocol.StreamEvent
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &events) != nil || len(events) != want {
					t.Fatalf("events %s: %d %s", query, w.Code, w.Body)
				}
				return events
			}
			check("", 500)
			rawCount := 506
			if engine == session.Codex {
				rawCount = 505
			}
			events := check("?limit=1000&view=raw", rawCount) // creation event is retained in raw history
			check("?limit=2000&view=raw", len(events))
			check("?after=0&limit=2&view=raw", 2)
			check("?before="+fmt.Sprint(events[len(events)-1].Sequence)+"&limit=3&view=raw", 3)
			for _, query := range []string{"?before=1&after=0", "?after=-1", "?before=", "?limit=0", "?limit=2&limit=3", "?view=invalid"} {
				f.call("GET", f.path("/events")+query, "", 400)
			}
			f.call("GET", f.path("/usage"), "", 200)
			unsupported := "/confirm-stopped"
			if engine != session.Codex {
				unsupported = "/status"
			}
			method := "POST"
			if unsupported == "/status" {
				method = "GET"
			}
			result := f.call(method, f.path(unsupported), `{}`, 409)
			if !reflect.DeepEqual(result, map[string]any{"code": "CAPABILITY_UNSUPPORTED", "error": "session action is not supported"}) {
				t.Fatal(result)
			}
			if engine != session.Codex {
				f.exec("UPDATE servers SET agent_version='0.0.0' WHERE id=?", f.serverID)
				result = f.call("POST", f.path("/steer"), `{}`, 409)
				if result["code"] != "CAPABILITY_UNSUPPORTED" {
					t.Fatal(result)
				}
			}
			f.call("GET", strings.Replace(f.path(""), engine, "unknown", 1), "", 400)
		})
	}
}

func TestUnifiedSessionAnswersAndAudit(t *testing.T) {
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		t.Run(engine, func(t *testing.T) {
			f := newSessionContract(t, engine)
			if engine == session.Codex {
				if f.call("POST", f.path("/questions/unknown/answer"), `{}`, 409)["code"] != "CAPABILITY_UNSUPPORTED" {
					t.Fatal("unsupported answer")
				}
				return
			}
			f.call("POST", f.path("/turns"), `{"prompt":"ask"}`, 202)
			ops := f.operations()
			if len(ops) != 1 {
				t.Fatal(ops)
			}
			f.exec("UPDATE agent_operations SET status='delivered' WHERE id=?", ops[0].ID)
			f.exec("UPDATE "+engine+"_threads SET status='running' WHERE id=?", f.id)
			question := f.add(engine+".question", fmt.Sprintf(`{"operation_id":%q,"questions":[{"id":"choice","question":"Which?","isOther":true}]}`, ops[0].ID))
			path := f.path("/questions/" + question.EventID + "/answer")
			f.call("POST", path, `{"answers":{}}`, 409)
			first := f.call("POST", path, `{"answers":{"choice":{"answers":["A"]}}}`, 202)
			again := f.call("POST", path, `{"answers":{"choice":{"answers":["A"]}}}`, 202)
			if !reflect.DeepEqual(first, again) {
				t.Fatal(first, again)
			}
			operation, e := f.db.Operation(context.Background(), first["operation_id"].(string))
			if e != nil || operation.Kind != engine+".question.answer" || operation.WorkspaceWrite != 0 {
				t.Fatal(operation, e)
			}
			var count int
			if e = f.db.DB.GetContext(context.Background(), &count, f.db.Q("SELECT COUNT(*) FROM audit_log WHERE action=? AND resource_id=?"), engine+".turn.start", f.id); e != nil || count != 1 {
				t.Fatalf("audit name: %d %v", count, e)
			}
		})
	}
}

func TestRetiredSessionRoutesAreUnavailable(t *testing.T) {
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		t.Run(engine, func(t *testing.T) {
			f := newSessionContract(t, engine)
			prefix := "/api/" + engine + "/threads"
			if engine == session.Codex {
				prefix = "/api/threads"
			}
			for _, old := range []struct{ method, path, body string }{
				{"GET", prefix, ""}, {"POST", prefix, fmt.Sprintf(`{"workspace_id":%q}`, f.workspaceID)},
				{"GET", prefix + "/" + f.id, ""}, {"PATCH", prefix + "/" + f.id, `{"title":"must not change"}`},
				{"DELETE", prefix + "/" + f.id, ""}, {"GET", prefix + "/" + f.id + "/events", ""},
				{"POST", prefix + "/" + f.id + "/turns", `{"prompt":"must not queue"}`},
			} {
				if w := f.request(old.method, old.path, old.body); w.Code != 404 {
					t.Fatalf("retired %s %s returned %d: %s", old.method, old.path, w.Code, w.Body)
				}
			}
			f.call("GET", f.path(""), "", 200)
		})
	}
}
