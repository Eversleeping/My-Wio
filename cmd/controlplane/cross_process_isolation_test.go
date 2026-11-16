package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/httpapi"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/sandbox"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.Command {
		if err := sandbox.Run(os.Args[2:]); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCrossProcessSessionIsolation(t *testing.T) {
	options := sandbox.Options{Bwrap: os.Getenv("WIO_SANDBOX_TEST_BWRAP")}
	if os.Geteuid() == 0 {
		options.UID, options.GID = 1000, 1000
	}
	probe, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := sandbox.Probe(probe, options)
	cancel()
	if err != nil {
		if os.Getenv("WIO_SANDBOX_TEST_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skipf("complete session isolation is unavailable: %v", err)
	}
	root, err := os.MkdirTemp("", "wio-cross-isolation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	fakeDir := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(fakeDir, 0755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(fakeDir, "fake-engine")
	if source := os.Getenv("WIO_ACCEPTANCE_FAKE_ENGINE"); source != "" {
		data, err := os.ReadFile(source)
		if err != nil || os.WriteFile(fake, data, 0755) != nil {
			t.Fatal("cannot prepare the prebuilt synthetic engine", err)
		}
	} else {
		buildFakeEngine(t, fake)
	}
	for _, engine := range []string{"codex", "claude", "gemini"} {
		if err := os.Symlink("fake-engine", filepath.Join(fakeDir, engine)); err != nil {
			t.Fatal(err)
		}
	}
	isolateAcceptanceUserDirectories(t, root)
	repositoryRoot := filepath.Join(root, "repositories")
	repository := filepath.Join(repositoryRoot, "isolated")
	if err := os.MkdirAll(repository, 0755); err != nil {
		t.Fatal(err)
	}
	runLocalCommand(t, repository, "git", "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("synthetic isolated workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "private-credential")
	if err := os.WriteFile(outside, []byte("synthetic upstream private value"), 0600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(root, "codex.key")
	if err := os.WriteFile(keyFile, []byte("synthetic-api-key"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	if err := os.WriteFile(configPath, []byte("model_provider = 'synthetic'\n[model_providers.synthetic]\nbase_url = 'http://127.0.0.1:1/v1'\nenv_key = 'WIO_CODEX_API_KEY'\nwire_api = 'responses'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WIO_MASTER_KEY", "synthetic-parent-only-master-key")
	t.Setenv("WIO_DATABASE_URL", "synthetic-parent-only-database")
	t.Setenv("WIO_SETUP_TOKEN", acceptanceSetupToken)
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-api-key")
	t.Setenv("GEMINI_API_KEY", "synthetic-api-key")
	t.Setenv("WIO_CONTROL_AGENT_URL", "")
	t.Setenv("WIO_CONTROL_AGENT_SCAN_ROOTS", repositoryRoot)
	t.Setenv("WIO_CONTROL_AGENT_CLONE_ROOT", filepath.Join(root, "clones"))
	t.Setenv("WIO_CONTROL_AGENT_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("WIO_CONTROL_AGENT_CODEX_PATH", filepath.Join(fakeDir, "codex"))
	t.Setenv("WIO_CONTROL_AGENT_CODEX_KEY_FILE", keyFile)
	t.Setenv("WIO_CONTROL_AGENT_PREREQUISITE_SOCKET", filepath.Join(root, "prerequisite.sock"))
	t.Setenv("WIO_CONTROL_AGENT_SESSION_ISOLATION", "true")
	t.Setenv("WIO_CONTROL_AGENT_ISOLATION_BWRAP", options.Bwrap)
	if os.Geteuid() == 0 {
		t.Setenv("WIO_CONTROL_AGENT_ISOLATION_UID", "1000")
		t.Setenv("WIO_CONTROL_AGENT_ISOLATION_GID", "1000")
		if err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(path, 1000, 1000)
		}); err != nil {
			t.Fatal(err)
		}
	}
	database, err := store.Open(filepath.Join(root, "wio.db") + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	var logOutput io.Writer = io.Discard
	if os.Getenv("WIO_SANDBOX_TEST_REQUIRED") == "1" {
		logOutput = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logOutput, nil))
	vault := security.DevVault()
	token, err := ensureControlPlaneAgentToken(context.Background(), database, vault, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureControlPlaneServer(context.Background(), "isolation-control-plane", token); err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	gateway := agentgateway.New(database, hub, vault, log)
	frontend, _ := fs.Sub(fstest.MapFS{"dist/index.html": {Data: []byte("synthetic frontend")}}, "dist")
	grpcServer, handler, lifecycle := controlPlaneHandler(database, hub, gateway, vault, log, frontend, "http://127.0.0.1", true, httpapi.TrustedProxies{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: handler}
	go httpServer.Serve(listener)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if joined, err := lifecycle.Shutdown(ctx, httpServer, grpcServer, gateway); !joined || err != nil {
			t.Errorf("isolation acceptance shutdown: joined=%v error=%v", joined, err)
		}
	})
	config, err := controlPlaneAgentConfig(listener.Addr().String(), token)
	if err != nil {
		t.Fatal(err)
	}
	running := startAcceptanceAgent(config, log)
	t.Cleanup(func() { stopAcceptanceAgent(t, running) })
	jar, _ := cookiejar.New(nil)
	api := &acceptanceAPI{baseURL: "http://" + listener.Addr().String(), client: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	api.setupAndLogin(t)
	var workspace store.Workspace
	eventually(t, 60*time.Second, "isolated Agent heartbeat and workspace", func() (bool, string) {
		servers, err := database.ListServers(context.Background())
		if err != nil {
			return false, err.Error()
		}
		if len(servers) != 1 || servers[0].SessionIsolation == nil {
			return false, "waiting for actual isolation status"
		}
		if status := servers[0].SessionIsolation; status.Mode != "enabled" {
			return false, "isolation state: " + status.Mode + ": " + status.Reason
		}
		response, err := api.request(http.MethodGet, "/api/workspaces", nil)
		if err != nil || response.status != http.StatusOK {
			return false, responseDetail(response, err)
		}
		var workspaces []store.Workspace
		if json.Unmarshal(response.body, &workspaces) != nil {
			return false, "invalid workspace response"
		}
		for _, row := range workspaces {
			if row.Path == repository {
				workspace = row
			}
		}
		return workspace.ID != "", "waiting for synthetic workspace inventory"
	})
	for _, name := range []string{"codex", "claude", "gemini"} {
		t.Run(name, func(t *testing.T) {
			fields := map[string]any{"workspace_id": workspace.ID}
			if name == "claude" {
				fields["model"], fields["permission_mode"] = "sonnet", "default"
			} else if name == "gemini" {
				fields["model"], fields["approval_mode"] = "flash", "default"
			}
			engine := &acceptanceEngine{name: name, collection: "/api/sessions/" + name, reply: "P0-07 fake " + map[string]string{"codex": "Codex", "claude": "Claude", "gemini": "Gemini"}[name] + " reply", createFields: fields}
			engine.create(t, api)
			report := filepath.Join(repository, name+"-isolation.json")
			raw, _ := json.Marshal(map[string]string{"Outside": outside, "Report": report})
			engine.start(t, api, "P5-04-PROBE="+base64.RawURLEncoding.EncodeToString(raw))
			engine.waitFor(t, api, "idle", 1)
			data, err := os.ReadFile(report)
			var proof map[string]bool
			if err != nil || json.Unmarshal(data, &proof) != nil || len(proof) != 3 {
				t.Fatal("missing isolation proof from the actual engine process")
			}
			for name, passed := range proof {
				if !passed {
					t.Errorf("cross-process isolation failed: %s", name)
				}
			}
		})
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "synthetic upstream private value" {
		t.Fatal("engine changed a private file outside its workspace")
	}
}
