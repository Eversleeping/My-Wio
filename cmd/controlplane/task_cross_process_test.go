package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/httpapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
	"github.com/wio-platform/wio/internal/task"
)

func taskAcceptanceJSON[T any](t *testing.T, api *acceptanceAPI, method, path string, body any, status int) T {
	t.Helper()
	response, err := api.request(method, path, body)
	if err != nil || response.status != status {
		t.Fatal(method, path, responseDetail(response, err))
	}
	var result T
	if err := json.Unmarshal(response.body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTaskThreeEngineRestartAcceptance(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native Agent acceptance uses Linux subprocesses")
	}
	ctx := context.Background()
	root := t.TempDir()
	fakeDir := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(fakeDir, 0755); err != nil {
		t.Fatal(err)
	}
	buildFakeEngine(t, filepath.Join(fakeDir, "fake-engine"))
	for _, name := range []string{"codex", "claude", "gemini"} {
		if err := os.Symlink("fake-engine", filepath.Join(fakeDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	isolateAcceptanceUserDirectories(t, root)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WIO_SETUP_TOKEN", acceptanceSetupToken)
	repos := filepath.Join(root, "repos")
	repo := filepath.Join(repos, "main")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	runLocalCommand(t, repo, "git", "init", "--initial-branch=main")
	for key, value := range map[string]string{"WIO_CONTROL_AGENT_URL": "", "WIO_CONTROL_AGENT_SCAN_ROOTS": root, "WIO_CONTROL_AGENT_CLONE_ROOT": filepath.Join(root, "clones"), "WIO_CONTROL_AGENT_STATE_DIR": filepath.Join(root, "agent-state"), "WIO_CONTROL_AGENT_CODEX_PATH": filepath.Join(fakeDir, "codex"), "WIO_CONTROL_AGENT_CODEX_KEY_FILE": filepath.Join(root, "codex.key"), "WIO_CONTROL_AGENT_PREREQUISITE_SOCKET": filepath.Join(root, "prerequisite.sock")} {
		t.Setenv(key, value)
	}
	dsn := filepath.Join(root, "tasks.db") + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	database, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault := security.DevVault()
	token, err := ensureControlPlaneAgentToken(ctx, database, vault, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureControlPlaneServer(ctx, "task-acceptance", token); err != nil {
		t.Fatal(err)
	}
	address := "127.0.0.1:0"
	var stopPlane func()
	startPlane := func() {
		hub := realtime.New()
		gateway := agentgateway.New(database, hub, vault, log)
		grpcServer, handler, lifecycle := controlPlaneHandler(database, hub, gateway, vault, log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://127.0.0.1", true, httpapi.TrustedProxies{})
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		address = listener.Addr().String()
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		lifecycle.Go(gateway.RunTasks)
		stopPlane = func() {
			shutdown, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if joined, err := lifecycle.Shutdown(shutdown, server, grpcServer, gateway); err != nil || !joined {
				t.Fatalf("task control plane shutdown: %v %v", joined, err)
			}
			<-done
		}
	}
	startPlane()
	t.Cleanup(func() {
		if stopPlane != nil {
			stopPlane()
		}
	})
	config, err := controlPlaneAgentConfig(address, token)
	if err != nil {
		t.Fatal(err)
	}
	running := startAcceptanceAgent(config, log)
	t.Cleanup(func() { stopAcceptanceAgent(t, running) })
	jar, _ := cookiejar.New(nil)
	api := &acceptanceAPI{baseURL: "http://" + address, client: &http.Client{Jar: jar, Timeout: 3 * time.Second}}
	api.setupAndLogin(t)
	var workspace store.Workspace
	eventually(t, 15*time.Second, "task Agent inventory", func() (bool, string) {
		workspaces, err := database.ListWorkspaces(ctx)
		if err != nil {
			return false, err.Error()
		}
		for _, w := range workspaces {
			if filepath.Clean(w.Path) == filepath.Clean(repo) {
				workspace = w
			}
		}
		server, err := database.Server(ctx, store.ControlPlaneServerID)
		return err == nil && workspace.ID != "" && server.Status == "online" && server.CodexReady == 1 && server.GeminiReady == 1 && server.ClaudeReady == 1, fmt.Sprintf("workspace=%s server=%s err=%v", workspace.ID, server.Status, err)
	})
	workspaces := []string{workspace.ID}
	for i := 1; i < 3; i++ {
		path := filepath.Join(root, fmt.Sprint("task-workspace-", i))
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		runLocalCommand(t, path, "git", "init", "--initial-branch=main")
		id := store.NewID()
		if _, err := database.DB.Exec(database.Q("INSERT INTO workspaces(id,project_id,server_id,path,status) VALUES(?,?,?,?,'ready')"), id, workspace.ProjectID, workspace.ServerID, path); err != nil {
			t.Fatal(err)
		}
		workspaces = append(workspaces, id)
	}
	for _, engine := range []string{"codex", "gemini", "claude"} {
		profile := store.NewID()
		if _, err := database.DB.Exec(database.Q("INSERT INTO credential_profiles(id,kind,name,endpoint,ciphertext) VALUES(?,?,?,'','synthetic-fake-engine-only')"), profile, engine, profile); err != nil {
			t.Fatal(err)
		}
		if _, err := database.DB.Exec(database.Q("UPDATE server_engines SET credential_profile_id=? WHERE server_id=? AND engine=?"), profile, workspace.ServerID, engine); err != nil {
			t.Fatal(err)
		}
	}
	created := taskAcceptanceJSON[struct {
		Task task.Task `json:"task"`
	}](t, api, "POST", "/api/tasks", task.Create{Meta: task.Meta{RequestID: "accept-create"}, ProjectID: workspace.ProjectID, Title: "Three engine task", Requirement: "Plan then develop and research, then summarize", Criteria: []task.Criterion{{ID: "done", Text: "Every required engine executes"}}}, 201).Task
	path := "/api/tasks/" + created.ID
	scope := "?project_id=" + workspace.ProjectID
	completionGate := filepath.Join(root, "complete-first-turn")
	plan := task.AppendPlan{Meta: task.Meta{RequestID: "accept-plan", ExpectedVersion: 1}, Reason: "explicit integration-test plan", Members: []task.Member{{ID: "coordinator", Role: "coordinator", Engine: "codex", WorkspaceID: workspaces[0], Mode: "on-request", DirectoryMode: "shared"}, {ID: "developer", Role: "developer", Engine: "claude", WorkspaceID: workspaces[1], Model: "sonnet", Mode: "default", DirectoryMode: "shared"}, {ID: "researcher", Role: "researcher", Engine: "gemini", WorkspaceID: workspaces[2], Model: "flash", Mode: "default", DirectoryMode: "shared"}}, Steps: []task.Step{
		{ID: "a", MemberID: "coordinator", Kind: "plan", Title: "Plan", Instructions: "P7-03 plan P0-07-RELEASE=" + base64.RawURLEncoding.EncodeToString([]byte(completionGate)), Required: true, CompletionPolicy: "execution"},
		{ID: "b", MemberID: "developer", Kind: "develop", Title: "Develop", Instructions: "P7-03 develop", Required: true, CompletionPolicy: "execution", DependsOn: []string{"a"}},
		{ID: "c", MemberID: "researcher", Kind: "research", Title: "Research", Instructions: "P7-03 research", Required: true, CompletionPolicy: "execution", DependsOn: []string{"a"}},
		{ID: "d", MemberID: "coordinator", Kind: "summarize", Title: "Summarize", Instructions: "P7-03 summarize", Required: true, CompletionPolicy: "execution", DependsOn: []string{"b", "c"}},
	}}
	p := taskAcceptanceJSON[task.PlanResult](t, api, "POST", path+"/plans"+scope, plan, 201)
	grant := task.GrantInput{Meta: task.Meta{RequestID: "accept-grant", ExpectedVersion: p.TaskVersion}, Enabled: true, Budgets: task.DefaultBudgets(), WorkspaceIDs: workspaces, ExecutionModes: map[string]task.ExecutionMode{}}
	for _, m := range plan.Members {
		grant.AllowedMembers = append(grant.AllowedMembers, m.ID)
		grant.ExecutionModes[m.ID] = task.ExecutionMode{Engine: m.Engine, Mode: m.Mode}
	}
	g := taskAcceptanceJSON[task.GrantResult](t, api, "PUT", path+"/grant"+scope, grant, 200)
	start := task.Start{Meta: task.Meta{RequestID: "accept-start", ExpectedVersion: g.TaskVersion}, PlanID: p.Plan.ID, GrantRevision: g.Grant.Revision}
	r := taskAcceptanceJSON[task.RunResult](t, api, "POST", path+"/runs"+scope, start, 201)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		var events []struct {
			Kind     string `db:"kind"`
			Sequence int64  `db:"sequence"`
		}
		err := database.DB.Select(&events, database.Q("SELECT kind,sequence FROM events WHERE stream_id IN (SELECT session_id FROM task_attempts WHERE run_id=?) ORDER BY stream_id,sequence"), r.Run.ID)
		t.Logf("native task events at failure: %+v (query error: %v)", events, err)
	})
	var firstOperation string
	eventually(t, 15*time.Second, "first real task operation", func() (bool, string) {
		err := database.DB.Get(&firstOperation, database.Q("SELECT operation_id FROM task_attempts WHERE run_id=? AND step_id='a'"), r.Run.ID)
		return err == nil && firstOperation != "", fmt.Sprint(err)
	})
	// Hold the fixture inside turn/start until the replacement gateway has
	// processed a heartbeat. Otherwise the instant fake completion can race
	// transport shutdown, which tests unacknowledged event delivery rather than
	// recovery of an operation that is still executing across the restart.
	eventually(t, 15*time.Second, "Codex executing before control plane restart", func() (bool, string) {
		_, err := os.Stat(completionGate + ".started")
		return err == nil, fmt.Sprint(err)
	})
	// Replace the entire HTTP/gRPC/gateway/worker instance and reopen its DB.
	// The same Agent reconnects; the old worker's lease and operation survive.
	stopPlane()
	stopPlane = nil
	reconnectAfter := time.Now().UTC()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	startPlane()
	replayed := taskAcceptanceJSON[task.RunResult](t, api, "POST", path+"/runs"+scope, start, 201)
	if replayed.Run.ID != r.Run.ID {
		t.Fatal("restart duplicated the task run")
	}
	eventually(t, 15*time.Second, "Agent reconnected after control plane restart", func() (bool, string) {
		server, err := database.Server(ctx, store.ControlPlaneServerID)
		return err == nil && server.LastSeenAt != nil && server.LastSeenAt.After(reconnectAfter), fmt.Sprintf("heartbeat=%v err=%v", server.LastSeenAt, err)
	})
	var acceptedBeforeRelease int
	if err := database.DB.Get(&acceptedBeforeRelease, database.Q("SELECT COUNT(*) FROM events WHERE stream_id IN (SELECT session_id FROM task_attempts WHERE run_id=?) AND kind=?"), r.Run.ID, protocol.EventTurnAccepted); err != nil || acceptedBeforeRelease != 0 {
		t.Fatal("Codex fixture completed turn/start before release", acceptedBeforeRelease, err)
	}
	if err := os.WriteFile(completionGate, []byte("complete\n"), 0600); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "three engine task after control plane restart", func() (bool, string) {
		response, err := api.request("GET", path+scope, nil)
		if err != nil {
			return false, err.Error()
		}
		var d task.Detail
		if err := json.Unmarshal(response.body, &d); err != nil {
			return false, err.Error()
		}
		return d.Run != nil && d.Run.State == "awaiting_acceptance", string(response.body)
	})
	d := taskAcceptanceJSON[task.Detail](t, api, "GET", path+scope, nil, 200)
	if len(d.Attempts) != 4 {
		t.Fatalf("attempts after restart: %d", len(d.Attempts))
	}
	engines := map[string]bool{}
	for _, a := range d.Attempts {
		engines[a.Engine] = true
		if a.State != "succeeded" || a.OperationID == nil {
			t.Fatal("missing native operation evidence", a)
		}
		if a.StepID == "a" && *a.OperationID != firstOperation {
			t.Fatal("restart replaced the original operation")
		}
	}
	if len(engines) != 3 {
		t.Fatal("not all three engines executed")
	}
	if d.AcceptanceEvidenceHash == "" || d.Run.AcceptanceReason != nil {
		t.Fatal("execution was treated as acceptance or evidence digest was missing")
	}
	acceptance := task.Acceptance{Meta: task.Meta{RequestID: "accept-user-decision", ExpectedVersion: d.Run.Version},
		Decision: "accepted", Reason: "Reviewed all three native sessions", EvidenceHash: d.AcceptanceEvidenceHash}
	type acceptanceResponse struct {
		Run task.Run `json:"run"`
	}
	actionPath := path + "/runs/" + d.Run.ID + "/acceptance" + scope
	out := taskAcceptanceJSON[acceptanceResponse](t, api, "POST", actionPath, acceptance, 200)
	repeat := taskAcceptanceJSON[acceptanceResponse](t, api, "POST", actionPath, acceptance, 200)
	if out.Run.State != "accepted" || repeat.Run.ID != out.Run.ID || repeat.Run.Version != out.Run.Version {
		t.Fatal("user decision did not persist and replay")
	}

}
