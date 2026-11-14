package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/httpapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

const (
	acceptanceSetupToken = "p0-07-setup-token"
	acceptancePassword   = "p0-07-test-password"
)

type acceptanceResponse struct {
	status int
	body   []byte
}

type acceptanceAPI struct {
	baseURL string
	client  *http.Client
	csrf    string
}

type acceptanceAgent struct {
	cancel context.CancelFunc
	done   chan error
}

type acceptanceEngine struct {
	name         string
	collection   string
	reply        string
	createFields map[string]any
	threadID     string
}

func TestCrossProcessAcceptance(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Agent and subprocess lifecycle under test are supported only on Linux")
	}
	root, err := os.MkdirTemp("", "wio-p0-07-")
	if err != nil {
		t.Fatal(err)
	}
	// Client.Run can return before operation goroutines finish writing receipts.
	// Control-plane shutdown also does not join the gateway receive/handler
	// goroutines, whose connection cleanup and in-flight SQLite updates can finish
	// after Store.Close returns. Retry removal until both kinds of writes stop.
	t.Cleanup(func() { removeAcceptanceRoot(t, root) })
	fakeDir := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(fakeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	buildFakeEngine(t, filepath.Join(fakeDir, "fake-engine"))
	for _, name := range []string{"codex", "claude", "gemini"} {
		if err := os.Symlink("fake-engine", filepath.Join(fakeDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	isolateAcceptanceUserDirectories(t, root)

	repositoryRoot := filepath.Join(root, "repositories")
	repository := filepath.Join(repositoryRoot, "acceptance")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runLocalCommand(t, repository, "git", "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("cross-process acceptance\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WIO_SETUP_TOKEN", acceptanceSetupToken)
	t.Setenv("WIO_CONTROL_AGENT_URL", "")
	t.Setenv("WIO_CONTROL_AGENT_SCAN_ROOTS", repositoryRoot)
	t.Setenv("WIO_CONTROL_AGENT_CLONE_ROOT", filepath.Join(root, "clones"))
	t.Setenv("WIO_CONTROL_AGENT_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("WIO_CONTROL_AGENT_CODEX_PATH", filepath.Join(fakeDir, "codex"))
	t.Setenv("WIO_CONTROL_AGENT_CODEX_KEY_FILE", filepath.Join(root, "codex.key"))
	t.Setenv("WIO_CONTROL_AGENT_PREREQUISITE_SOCKET", filepath.Join(root, "prerequisite.sock"))

	// modernc.org/sqlite v1.31.1 can strand a read statement when the stream
	// context is canceled after its first row. In rollback-journal mode that
	// reader permanently blocks later writes. WAL readers do not block a writer,
	// keeping this transport-lifecycle test deterministic while that product
	// defect is tracked separately.
	database, err := store.Open(filepath.Join(root, "wio.db") + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault := security.DevVault()
	token, err := ensureControlPlaneAgentToken(context.Background(), database, vault, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureControlPlaneServer(context.Background(), "p0-07-control-plane", token); err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	gateway := agentgateway.New(database, hub, vault, log)
	frontend, err := fs.Sub(fstest.MapFS{"dist/index.html": {Data: []byte("ok")}}, "dist")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer, handler := controlPlaneHandler(database, hub, gateway, vault, log, frontend, "http://127.0.0.1", true, httpapi.TrustedProxies{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- httpServer.Serve(listener) }()
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownContext)
		grpcServer.Stop()
		select {
		case <-serverDone:
		case <-shutdownContext.Done():
		}
	})

	config, err := controlPlaneAgentConfig(listener.Addr().String(), token)
	if err != nil {
		t.Fatal(err)
	}
	runningAgent := startAcceptanceAgent(config, log)
	t.Cleanup(func() { stopAcceptanceAgent(t, runningAgent) })

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	api := &acceptanceAPI{baseURL: "http://" + listener.Addr().String(), client: &http.Client{Jar: jar, Timeout: 3 * time.Second}}
	api.setupAndLogin(t)

	var workspace store.Workspace
	eventually(t, 10*time.Second, "built-in Agent and inventory", func() (bool, string) {
		serversResponse, err := api.request(http.MethodGet, "/api/servers", nil)
		if err != nil || serversResponse.status != http.StatusOK {
			return false, responseDetail(serversResponse, err)
		}
		var servers []store.Server
		if err := json.Unmarshal(serversResponse.body, &servers); err != nil {
			return false, err.Error()
		}
		online := false
		for _, server := range servers {
			if server.ID == store.ControlPlaneServerID && server.Status == "online" && server.CodexReady == 1 && server.ClaudeReady == 1 && server.GeminiReady == 1 {
				online = true
			}
		}
		workspacesResponse, err := api.request(http.MethodGet, "/api/workspaces", nil)
		if err != nil || workspacesResponse.status != http.StatusOK {
			return false, responseDetail(workspacesResponse, err)
		}
		var workspaces []store.Workspace
		if err := json.Unmarshal(workspacesResponse.body, &workspaces); err != nil {
			return false, err.Error()
		}
		for _, candidate := range workspaces {
			if filepath.Clean(candidate.Path) == filepath.Clean(repository) {
				workspace = candidate
			}
		}
		return online && workspace.ID != "", fmt.Sprintf("online=%v workspaces=%s", online, workspacesResponse.body)
	})

	engines := []*acceptanceEngine{
		{name: "codex", collection: "/api/threads", reply: "P0-07 fake Codex reply", createFields: map[string]any{"workspace_id": workspace.ID}},
		{name: "claude", collection: "/api/claude/threads", reply: "P0-07 fake Claude reply", createFields: map[string]any{"workspace_id": workspace.ID, "model": "sonnet", "permission_mode": "default"}},
		{name: "gemini", collection: "/api/gemini/threads", reply: "P0-07 fake Gemini reply", createFields: map[string]any{"workspace_id": workspace.ID, "model": "flash", "approval_mode": "default"}},
	}
	for _, engine := range engines {
		engine.create(t, api)
		engine.start(t, api, "P0-07 normal reply")
		engine.waitFor(t, api, "idle", 1)

		marker := filepath.Join(root, engine.name+"-hang")
		prompt := "P0-07 hang P0-07-HANG=" + base64.RawURLEncoding.EncodeToString([]byte(marker))
		engine.start(t, api, prompt)
		var pid int
		eventually(t, 10*time.Second, engine.name+" hanging turn", func() (bool, string) {
			threadStatus, detail := engine.status(api)
			raw, readErr := os.ReadFile(marker)
			if readErr == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0]))
			}
			return threadStatus == "running" && pid > 0, detail
		})
		engine.interrupt(t, api)
		engine.waitFor(t, api, "interrupted", 1)
		if engine.name == "codex" {
			eventually(t, 5*time.Second, "Codex turn cleared without stopping app-server", func() (bool, string) {
				raw, err := os.ReadFile(marker)
				return err == nil && strings.Contains(string(raw), "interrupted") && processExists(pid), string(raw)
			})
			engine.start(t, api, "P0-07 reply after interrupt")
			engine.waitFor(t, api, "idle", 2)
		} else {
			eventually(t, 5*time.Second, engine.name+" process exit", func() (bool, string) {
				return !processExists(pid), fmt.Sprintf("pid %d still exists", pid)
			})
		}
	}

	refreshWorkspaceFiles(t, api, workspace.ID, "README.md")
	if err := os.WriteFile(filepath.Join(repository, "reconnected.txt"), []byte("queued while disconnected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopAcceptanceAgent(t, runningAgent)
	runningAgent = nil
	queued, err := api.request(http.MethodPost, "/api/workspaces/"+workspace.ID+"/files/refresh", map[string]any{})
	if err != nil || queued.status != http.StatusAccepted {
		t.Fatalf("queue disconnected workspace operation: %s", responseDetail(queued, err))
	}
	runningAgent = startAcceptanceAgent(config, log)
	// Client.Run may return just before the gateway removes the old stream. If
	// Wake sends this operation through that stream, it is marked delivered but
	// not received; PendingOperations makes it eligible again after 30 seconds,
	// and the gateway polls at most two seconds later. The wider timeout covers
	// both that race and the usual queued-on-reconnect path without changing
	// either production interval.
	refreshWorkspaceFilesResult(t, api, workspace.ID, "reconnected.txt", 40*time.Second)
}

func isolateAcceptanceUserDirectories(t *testing.T, root string) {
	t.Helper()
	directories := map[string]string{
		"HOME":              filepath.Join(root, "user", "home"),
		"CODEX_HOME":        filepath.Join(root, "user", "codex"),
		"GEMINI_CLI_HOME":   filepath.Join(root, "user", "gemini"),
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "user", "claude"),
		"XDG_CONFIG_HOME":   filepath.Join(root, "user", "xdg-config"),
		"XDG_CACHE_HOME":    filepath.Join(root, "user", "xdg-cache"),
		"XDG_DATA_HOME":     filepath.Join(root, "user", "xdg-data"),
		"XDG_STATE_HOME":    filepath.Join(root, "user", "xdg-state"),
		"XDG_RUNTIME_DIR":   filepath.Join(root, "user", "xdg-runtime"),
		"TMPDIR":            filepath.Join(root, "tmp"),
		"TMP":               filepath.Join(root, "tmp"),
		"TEMP":              filepath.Join(root, "tmp"),
	}
	for name, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("prepare isolated %s: %v", name, err)
		}
		t.Setenv(name, directory)
	}
	t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", filepath.Join(root, "user", "gemini-system-settings.json"))
	t.Setenv("GEMINI_CLI_SYSTEM_DEFAULTS_PATH", filepath.Join(root, "user", "gemini-system-defaults.json"))
}

func removeAcceptanceRoot(t *testing.T, root string) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	var absentSince time.Time
	var lastRemoveErr error
	var lastInspectErr error
	for {
		_, err := os.Stat(root)
		switch {
		case os.IsNotExist(err):
			lastInspectErr = nil
			if absentSince.IsZero() {
				absentSince = time.Now()
			} else if time.Since(absentSince) >= 500*time.Millisecond {
				return
			}
		case err == nil:
			absentSince = time.Time{}
			lastInspectErr = nil
			if err := os.RemoveAll(root); err != nil {
				lastRemoveErr = err
			}
		default:
			absentSince = time.Time{}
			lastInspectErr = err
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			switch {
			case lastRemoveErr != nil && lastInspectErr != nil:
				t.Errorf("acceptance root cleanup did not stabilize: last remove error: %v; last inspect error: %v", lastRemoveErr, lastInspectErr)
			case lastRemoveErr != nil:
				t.Errorf("acceptance root cleanup did not stabilize: last remove error: %v", lastRemoveErr)
			case lastInspectErr != nil:
				t.Errorf("acceptance root cleanup did not stabilize: last inspect error: %v", lastInspectErr)
			default:
				t.Errorf("acceptance root cleanup did not stabilize")
			}
			return
		}
	}
}

func buildFakeEngine(t *testing.T, output string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate fake engine source")
	}
	command := exec.Command("go", "build", "-buildvcs=false", "-o", output, ".")
	command.Dir = filepath.Join(filepath.Dir(sourceFile), "testdata", "fakeengine")
	command.Env = replaceEnvironment(os.Environ(), map[string]string{"GOPROXY": "off"})
	outputBytes, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake engine: %v\n%s", err, outputBytes)
	}
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := replacements[key]; !replace {
			result = append(result, entry)
		}
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}

func runLocalCommand(t *testing.T, directory, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(arguments, " "), err, output)
	}
}

func startAcceptanceAgent(config agent.Config, log *slog.Logger) *acceptanceAgent {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	client := agent.NewClient(config, log)
	go func() { done <- client.Run(ctx) }()
	return &acceptanceAgent{cancel: cancel, done: done}
}

func stopAcceptanceAgent(t *testing.T, running *acceptanceAgent) {
	t.Helper()
	if running == nil {
		return
	}
	running.cancel()
	select {
	case err := <-running.done:
		if err != nil && err != context.Canceled {
			t.Errorf("Agent stopped with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("timed out stopping Agent")
	}
}

func (api *acceptanceAPI) setupAndLogin(t *testing.T) {
	t.Helper()
	setup, err := api.request(http.MethodPost, "/api/setup", map[string]string{"setup_token": acceptanceSetupToken, "username": "admin", "auth_mode": store.AuthModePassword, "password": acceptancePassword})
	if err != nil || setup.status != http.StatusCreated {
		t.Fatalf("setup: %s", responseDetail(setup, err))
	}
	login, err := api.request(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": acceptancePassword})
	if err != nil || login.status != http.StatusOK {
		t.Fatalf("login: %s", responseDetail(login, err))
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.body, &body); err != nil || body.CSRF == "" {
		t.Fatalf("login response has no CSRF token: %v %s", err, login.body)
	}
	api.csrf = body.CSRF
	if session, err := api.request(http.MethodGet, "/api/auth/session", nil); err != nil || session.status != http.StatusOK {
		t.Fatalf("authenticated session: %s", responseDetail(session, err))
	}
}

func (api *acceptanceAPI) request(method, path string, payload any) (acceptanceResponse, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return acceptanceResponse{}, err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, api.baseURL+path, body)
	if err != nil {
		return acceptanceResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if api.csrf != "" && method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("X-CSRF-Token", api.csrf)
	}
	response, err := api.client.Do(request)
	if err != nil {
		return acceptanceResponse{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	return acceptanceResponse{status: response.StatusCode, body: raw}, err
}

func (engine *acceptanceEngine) create(t *testing.T, api *acceptanceAPI) {
	t.Helper()
	response, err := api.request(http.MethodPost, engine.collection, engine.createFields)
	if err != nil || response.status != http.StatusCreated {
		t.Fatalf("create %s thread: %s", engine.name, responseDetail(response, err))
	}
	var thread struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.body, &thread); err != nil || thread.ID == "" {
		t.Fatalf("decode %s thread: %v %s", engine.name, err, response.body)
	}
	engine.threadID = thread.ID
}

func (engine *acceptanceEngine) start(t *testing.T, api *acceptanceAPI, prompt string) {
	t.Helper()
	response, err := api.request(http.MethodPost, engine.collection+"/"+engine.threadID+"/turns", map[string]string{"prompt": prompt})
	if err != nil || response.status != http.StatusAccepted {
		t.Fatalf("start %s turn: %s", engine.name, responseDetail(response, err))
	}
}

func (engine *acceptanceEngine) interrupt(t *testing.T, api *acceptanceAPI) {
	t.Helper()
	response, err := api.request(http.MethodPost, engine.collection+"/"+engine.threadID+"/interrupt", map[string]any{})
	if err != nil || response.status != http.StatusAccepted {
		t.Fatalf("interrupt %s turn: %s", engine.name, responseDetail(response, err))
	}
}

func (engine *acceptanceEngine) status(api *acceptanceAPI) (string, string) {
	response, err := api.request(http.MethodGet, engine.collection+"/"+engine.threadID, nil)
	if err != nil || response.status != http.StatusOK {
		return "", responseDetail(response, err)
	}
	var thread struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.body, &thread); err != nil {
		return "", err.Error()
	}
	return thread.Status, string(response.body)
}

func (engine *acceptanceEngine) waitFor(t *testing.T, api *acceptanceAPI, status string, replyCount int) {
	t.Helper()
	eventually(t, 10*time.Second, engine.name+" turn status "+status, func() (bool, string) {
		current, detail := engine.status(api)
		eventsResponse, err := api.request(http.MethodGet, engine.collection+"/"+engine.threadID+"/events", nil)
		if err != nil || eventsResponse.status != http.StatusOK {
			return false, responseDetail(eventsResponse, err)
		}
		var events []protocol.StreamEvent
		if err := json.Unmarshal(eventsResponse.body, &events); err != nil {
			return false, err.Error()
		}
		matches := 0
		for _, event := range events {
			if strings.Contains(string(event.Payload), engine.reply) {
				matches++
			}
		}
		return current == status && matches >= replyCount, fmt.Sprintf("thread=%s matches=%d events=%s", detail, matches, eventsResponse.body)
	})
}

func refreshWorkspaceFiles(t *testing.T, api *acceptanceAPI, workspaceID, expected string) {
	t.Helper()
	response, err := api.request(http.MethodPost, "/api/workspaces/"+workspaceID+"/files/refresh", map[string]any{})
	if err != nil || response.status != http.StatusAccepted {
		t.Fatalf("refresh workspace files: %s", responseDetail(response, err))
	}
	refreshWorkspaceFilesResult(t, api, workspaceID, expected, 10*time.Second)
}

func refreshWorkspaceFilesResult(t *testing.T, api *acceptanceAPI, workspaceID, expected string, timeout time.Duration) {
	t.Helper()
	eventually(t, timeout, "workspace file "+expected, func() (bool, string) {
		response, err := api.request(http.MethodGet, "/api/workspaces/"+workspaceID+"/files", nil)
		if err != nil || response.status != http.StatusOK {
			return false, responseDetail(response, err)
		}
		var snapshot struct {
			Status string                   `json:"status"`
			Files  []protocol.WorkspaceFile `json:"files"`
		}
		if err := json.Unmarshal(response.body, &snapshot); err != nil {
			return false, err.Error()
		}
		for _, file := range snapshot.Files {
			if file.Path == expected {
				return snapshot.Status == "succeeded", string(response.body)
			}
		}
		return false, string(response.body)
	})
}

func eventually(t *testing.T, timeout time.Duration, description string, condition func() (bool, string)) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	last := "condition not checked"
	for {
		ok, detail := condition()
		last = detail
		if ok {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s: %s", description, last)
		}
	}
}

func processExists(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

func responseDetail(response acceptanceResponse, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("status=%d body=%s", response.status, response.body)
}
