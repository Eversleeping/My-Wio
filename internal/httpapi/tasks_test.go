package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/task"
)

func TestTaskDraftAndPlanAPI(t *testing.T) {
	f := newSessionContract(t, "codex")
	ctx := context.Background()
	w, err := f.db.Workspace(ctx, f.workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	project := w.ProjectID
	body := fmt.Sprintf(`{"request_id":"create-task","project_id":%q,"title":"Feature","requirement":"Build a feature","criteria":[{"id":"works","text":"Feature works"}]}`, project)
	created := f.call("POST", "/api/tasks", body, 201)
	if replay := f.call("POST", "/api/tasks", body, 201); !reflect.DeepEqual(replay, created) {
		t.Fatal("create replay differs")
	}
	id := created["task"].(map[string]any)["id"].(string)
	path := "/api/tasks/" + id
	scope := "?project_id=" + project
	detail := f.call("GET", path+scope, "", 200)
	if detail["run"] != nil || detail["last_sequence"] != float64(1) || detail["capabilities"].(map[string]any)["start_run"] != false {
		t.Fatal(detail)
	}
	input := task.AppendPlan{Meta: task.Meta{RequestID: "plan-task", ExpectedVersion: 1}, Reason: "Explicit user plan", Members: []task.Member{{ID: "dev", Role: "developer", Engine: "codex", WorkspaceID: f.workspaceID, Mode: "on-request", DirectoryMode: "shared"}}, Steps: []task.Step{{ID: "build", MemberID: "dev", Kind: "develop", Title: "Build", Instructions: "Build feature", Required: true, CompletionPolicy: "execution"}}}
	raw, _ := json.Marshal(input)
	plan := f.call("POST", path+"/plans"+scope, string(raw), 201)
	if replay := f.call("POST", path+"/plans"+scope, string(raw), 201); !reflect.DeepEqual(plan, replay) {
		t.Fatal("plan replay differs")
	}
	if plan["task_version"] != float64(2) {
		t.Fatal(plan)
	}
	planID := plan["plan"].(map[string]any)["id"].(string)
	request := f.call("GET", "/api/task-requests/create-task"+scope, "", 200)
	if !reflect.DeepEqual(request["response"], created) {
		t.Fatal("request did not recover original response", request)
	}
	f.call("PATCH", path+scope, `{"request_id":"update-task","expected_version":2,"requirement":"New requirement"}`, 200)
	input.ExpectedVersion = 3
	input.ParentID = &planID
	input.RequestID = "invalid-graph"
	input.Steps[0].DependsOn = []string{"build"}
	raw, _ = json.Marshal(input)
	invalid := f.call("POST", path+"/plans"+scope, string(raw), 400)
	if invalid["code"] != task.Invalid {
		t.Fatal(invalid)
	}
	input.RequestID = "unsupported-policy"
	input.Steps[0].DependsOn = nil
	input.Steps[0].CompletionPolicy = "review"
	raw, _ = json.Marshal(input)
	unsupported := f.call("POST", path+"/plans"+scope, string(raw), 422)
	if unsupported["code"] != task.Unsupported {
		t.Fatal(unsupported)
	}
	stale := f.call("PATCH", path+scope, `{"request_id":"stale-task","expected_version":2,"title":"stale"}`, 409)
	if stale["code"] != task.Conflict {
		t.Fatal(stale)
	}
	conflict := f.call("POST", "/api/tasks", strings.Replace(body, "Feature", "Other feature", 1), 409)
	if conflict["code"] != task.IdempotencyConflict {
		t.Fatal(conflict)
	}
	first := f.call("GET", path+"/events"+scope+"&limit=1", "", 200)
	if first["next_after"] != float64(1) || first["has_more"] != true {
		t.Fatal(first)
	}
	second := f.call("GET", path+"/events"+scope+"&after=1", "", 200)
	if len(second["items"].([]any)) != 2 || second["has_more"] != false {
		t.Fatal(second)
	}
	page := f.call("GET", "/api/tasks"+scope+"&limit=1", "", 200)
	if len(page["items"].([]any)) != 1 {
		t.Fatal(page)
	}
	f.call("GET", path+"?project_id=another-project", "", 404)
	f.call("POST", path+"/plans?project_id=another-project", string(raw), 404)
	f.call("GET", path+"/events?project_id=another-project", "", 404)
	f.call("GET", "/api/task-requests/create-task?project_id=another-project", "", 404)
	for _, query := range []string{"", "?project_id=", "?project_id=" + project + "&project_id=" + project, scope + "&limit=0", scope + "&limit=1&limit=2", scope + "&cursor=bad", scope + "&state=bogus"} {
		f.call("GET", "/api/tasks"+query, "", 400)
	}
	f.call("POST", "/api/tasks", strings.TrimSuffix(body, "}")+`,"actor":"spoof"}`, 400)
	f.call("POST", "/api/tasks", body+" {}", 400)
	f.call("POST", "/api/tasks", `{"request_id":"large","project_id":"`+project+`","title":"`+strings.Repeat("x", 1<<20)+`"}`, 400)
	// Draft and plan commands alone never start execution.
	var runs int
	if err := f.db.DB.Get(&runs, "SELECT COUNT(*) FROM task_runs"); err != nil || runs != 0 {
		t.Fatal("draft API started execution", err)
	}
	operations, err := f.db.PendingOperations(ctx, f.serverID)
	if err != nil || len(operations) != 0 {
		t.Fatal("draft API enqueued Agent operations", err)
	}
	// Authentication and CSRF remain required for the new namespace.
	for _, test := range []struct {
		cookie, csrf bool
		want         int
	}{{false, false, 401}, {true, false, 403}} {
		r := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(body))
		if test.cookie {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "contract-cookie"})
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, r)
		if rec.Code != test.want {
			t.Fatalf("auth/CSRF: %d want %d", rec.Code, test.want)
		}
	}
}

func TestTaskExecutionAPI(t *testing.T) {
	f := newSessionContract(t, "codex")
	w, err := f.db.Workspace(context.Background(), f.workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	created := f.call("POST", "/api/tasks", fmt.Sprintf(`{"request_id":"execution-create","project_id":%q,"title":"Execute","requirement":"Build feature","criteria":[{"id":"works","text":"works"}]}`, w.ProjectID), 201)
	id := created["task"].(map[string]any)["id"].(string)
	path := "/api/tasks/" + id
	scope := "?project_id=" + w.ProjectID
	plan := task.AppendPlan{Meta: task.Meta{RequestID: "execution-plan", ExpectedVersion: 1}, Reason: "explicit plan", Members: []task.Member{{ID: "dev", Role: "developer", Engine: "codex", WorkspaceID: f.workspaceID, Mode: "on-request", DirectoryMode: "shared"}}, Steps: []task.Step{{ID: "build", MemberID: "dev", Kind: "develop", Title: "Build", Instructions: "Build feature", Required: true, CompletionPolicy: "execution"}}}
	raw, _ := json.Marshal(plan)
	p := f.call("POST", path+"/plans"+scope, string(raw), 201)
	planID := p["plan"].(map[string]any)["id"].(string)
	start := task.Start{Meta: task.Meta{RequestID: "execution-start", ExpectedVersion: 2}, PlanID: planID, GrantRevision: 1}
	raw, _ = json.Marshal(start)
	denied := f.call("POST", path+"/runs"+scope, string(raw), 403)
	if denied["code"] != task.Forbidden {
		t.Fatal(denied)
	}
	grant := task.GrantInput{Meta: task.Meta{RequestID: "execution-grant", ExpectedVersion: 2}, Enabled: true, AllowedMembers: []string{"dev"}, WorkspaceIDs: []string{f.workspaceID}, ExecutionModes: map[string]task.ExecutionMode{"dev": {Engine: "codex", Mode: "on-request"}}, Budgets: task.DefaultBudgets()}
	raw, _ = json.Marshal(grant)
	g := f.call("PUT", path+"/grant"+scope, string(raw), 200)
	if replay := f.call("PUT", path+"/grant"+scope, string(raw), 200); !reflect.DeepEqual(g, replay) {
		t.Fatal("grant replay")
	}
	start.ExpectedVersion = 3
	raw, _ = json.Marshal(start)
	r := f.call("POST", path+"/runs"+scope, string(raw), 201)
	if replay := f.call("POST", path+"/runs"+scope, string(raw), 201); !reflect.DeepEqual(r, replay) {
		t.Fatal("run replay")
	}
	reconciled := f.call("GET", "/api/task-requests/execution-start"+scope, "", 200)
	if !reflect.DeepEqual(reconciled["response"], r) {
		t.Fatal("start reconciliation")
	}
	run := r["run"].(map[string]any)
	runID := run["id"].(string)
	actionPath := path + "/runs/" + runID + "/actions"
	action := task.Action{Meta: task.Meta{RequestID: "execution-pause", ExpectedVersion: int64(run["version"].(float64))}, Action: "pause", Reason: "user pause"}
	raw, _ = json.Marshal(action)
	paused := f.call("POST", actionPath+scope, string(raw), 202)
	if replay := f.call("POST", actionPath+scope, string(raw), 202); !reflect.DeepEqual(paused, replay) {
		t.Fatal("action replay")
	}
	action.RequestID = "execution-stale"
	raw, _ = json.Marshal(action)
	stale := f.call("POST", actionPath+scope, string(raw), 409)
	if stale["code"] != task.Conflict {
		t.Fatal(stale)
	}
	for _, entry := range []struct {
		method, path string
		body         any
	}{{"PUT", path + "/grant", grant}, {"POST", path + "/runs", start}, {"POST", actionPath, action}} {
		raw, _ := json.Marshal(entry.body)
		f.call(entry.method, entry.path+"?project_id=foreign", string(raw), 404)
		f.call(entry.method, entry.path, string(raw), 400)
		for _, auth := range []struct {
			cookie bool
			want   int
		}{{false, 401}, {true, 403}} {
			req := httptest.NewRequest(entry.method, entry.path+scope, strings.NewReader(string(raw)))
			if auth.cookie {
				req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "contract-cookie"})
			}
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			if rec.Code != auth.want {
				t.Fatal("execution auth/CSRF", entry.path, rec.Code, auth.want)
			}
		}
	}
}

func TestTaskArchiveAndLocationAPI(t *testing.T) {
	f := newSessionContract(t, "codex")
	w, err := f.db.Workspace(context.Background(), f.workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	base := f.call("POST", "/api/tasks", fmt.Sprintf(`{"request_id":"archive-create","project_id":%q,"title":"Archive test","requirement":"Preserve history","criteria":[{"id":"works","text":"Stored"}]}`, w.ProjectID), 201)
	id := base["task"].(map[string]any)["id"].(string)
	path := "/api/tasks/" + id
	scope := "?project_id=" + w.ProjectID
	if location := f.call("GET", path+"/location", "", 200); location["project_id"] != w.ProjectID {
		t.Fatal(location)
	}
	if recent := f.call("GET", "/api/tasks/recent", "", 200); len(recent["items"].([]any)) != 1 {
		t.Fatal(recent)
	}
	body := `{"request_id":"archive-task","expected_version":1,"archived":true}`
	archived := f.call("POST", path+"/archive"+scope, body, 200)
	if archived["task"].(map[string]any)["archived_at"] == nil {
		t.Fatal(archived)
	}
	if replay := f.call("GET", "/api/task-requests/archive-task"+scope, "", 200); !reflect.DeepEqual(replay["response"], archived) {
		t.Fatal(replay)
	}
	f.call("POST", path+"/archive?project_id=other", body, 404)
	if recent := f.call("GET", "/api/tasks/recent", "", 200); len(recent["items"].([]any)) != 0 {
		t.Fatal(recent)
	}
	f.call("POST", path+"/archive"+scope, `{"request_id":"restore-task","expected_version":2,"archived":false}`, 200)
	f.call("GET", path+"/events"+scope, "", 200)
}
