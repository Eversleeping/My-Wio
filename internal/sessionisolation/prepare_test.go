package sessionisolation

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/sandbox"
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

func TestMissingBoundaryDoesNotLaunchCLIOrBlockAgentHelpers(t *testing.T) {
	root := t.TempDir()
	manager := New(&Config{Enabled: true, Bwrap: filepath.Join(root, "missing-bwrap")}, root)
	ctx := manager.Context(context.Background())
	cmd := childprocess.CommandContext(childprocess.WithScope(ctx, childprocess.Codex), "sh", "-c", "exit 0")
	release, err := childprocess.Prepare(childprocess.WithScope(ctx, childprocess.Codex), cmd)
	if release != nil {
		release()
	}
	if err == nil || cmd.Process != nil {
		t.Fatal("unavailable isolation allowed an official CLI to start")
	}
	gitContext := childprocess.WithScope(ctx, childprocess.Git)
	if err := childprocess.RunContext(gitContext, childprocess.CommandContext(gitContext, "sh", "-c", "exit 0")); err != nil {
		t.Fatalf("trusted Agent helper was affected by CLI isolation: %v", err)
	}
	if status := manager.Status(ctx); status.Mode != "unsupported" || status.Reason == "" {
		t.Fatal("unavailable boundary was not reported")
	}
	if status := New(nil, root).Status(ctx); status.Mode != "disabled" {
		t.Fatal("default isolation state changed")
	}
}

func TestOnlyReviewedClaudeDescriptorsReachTheCLI(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "descriptor")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd := &exec.Cmd{Args: []string{"claude", "--mcp-config", "/dev/fd/3"}, ExtraFiles: []*os.File{file}}
	if _, err := reviewedDescriptors(cmd, environment{}, "claude"); err != nil {
		t.Fatal("reviewed MCP descriptor was rejected")
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, file)
	if _, err := reviewedDescriptors(cmd, environment{}, "claude"); err == nil {
		t.Fatal("an unrelated inherited descriptor was accepted")
	}
	if _, err := reviewedDescriptors(cmd, environment{}, "codex"); err == nil {
		t.Fatal("a Claude descriptor was accepted by another engine")
	}
}

func TestPrivateTemporaryRangeHasOneOwnerAndIsRemoved(t *testing.T) {
	ctx := New(&Config{Enabled: true}, t.TempDir()).Context(context.Background())
	ctx, release, err := Temporary(ctx)
	if err != nil || TempDir(ctx) == "" {
		t.Fatal("private temporary range was not prepared")
	}
	root := TempDir(ctx)
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("private temporary range is not private")
	}
	again, borrowedRelease, err := Temporary(ctx)
	if err != nil || TempDir(again) != root {
		t.Fatal("an adapter expanded its temporary range")
	}
	borrowedRelease()
	if _, err := os.Stat(root); err != nil {
		t.Fatal("borrower removed another process's temporary range")
	}
	release()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("private temporary range leaked after release")
	}
}

func TestPublicRuntimeCannotExposeCredentialsOrAgentState(t *testing.T) {
	root := t.TempDir()
	home, state := filepath.Join(root, "home"), filepath.Join(root, "agent-state")
	for _, path := range []string{home, filepath.Join(home, ".codex"), filepath.Join(state, "gemini"), filepath.Join(state, "codex", "versions", "public")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	auth := authentication{home: home, state: filepath.Join(home, ".codex")}
	cmd := exec.Command("sh")
	for _, path := range []string{root, home, auth.state, state, filepath.Join(state, "gemini")} {
		if _, err := runtimeMounts(cmd, []string{path}, auth, state); err == nil {
			t.Fatal("public runtime range exposed a credential-bearing directory")
		}
	}
	if _, err := runtimeMounts(cmd, []string{filepath.Join(state, "codex", "versions", "public")}, auth, state); err != nil {
		t.Fatal("public CLI installation nested under Agent state was rejected")
	}
}

func TestPublicRuntimeCannotReplacePrivateNamespaces(t *testing.T) {
	auth := authentication{home: "/synthetic-agent-home", state: "/synthetic-agent-home/.codex"}
	for _, path := range []string{"/proc", "/sys", "/dev", "/run", "/var/run", "/tmp", "/var/tmp"} {
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skip("system range is absent")
			}
			if _, err := runtimeMounts(exec.Command("sh"), []string{path}, auth, ""); err == nil {
				t.Fatal("public runtime configuration replaced a private process, device, socket or temporary namespace")
			}
		})
	}
}

func TestConfiguredCredentialFilesCannotBecomeWorkspaceOrRuntime(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	state := filepath.Join(home, "agent-state")
	auth := authentication{home: home, state: filepath.Join(home, ".codex")}
	configuration, key, socket := filepath.Join(root, "agent", "config.json"), filepath.Join(root, "model", "access.key"), filepath.Join(root, "helper", "socket")
	manager := New(&Config{Enabled: true}, state, configuration, key, socket)
	public := filepath.Join(root, "public-runtime")
	workspace := filepath.Join(state, "projects", "workspace")
	for _, dir := range []string{filepath.Dir(configuration), filepath.Dir(key), filepath.Dir(socket), filepath.Join(state, "claude"), filepath.Join(state, "gemini"), public, workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Dir(configuration), filepath.Dir(key), filepath.Dir(socket), filepath.Join(state, "claude"), filepath.Join(state, "gemini")} {
		if _, err := runtimeMounts(exec.Command("sh"), []string{path}, auth, state, manager.protectedPaths...); err == nil {
			t.Fatal("a configured credential or helper range was exposed as a public runtime")
		}
		if err := guardWorkspace(path, auth, state, manager.protectedPaths...); err == nil {
			t.Fatal("a configured credential or helper range was exposed as a workspace")
		}
	}
	if _, err := runtimeMounts(exec.Command("sh"), []string{public}, auth, state, manager.protectedPaths...); err != nil {
		t.Fatalf("a separate staged public runtime was rejected: %v", err)
	}
	if err := guardWorkspace(workspace, auth, state, manager.protectedPaths...); err != nil {
		t.Fatalf("a normal workspace beneath Agent state was rejected: %v", err)
	}
	if err := os.WriteFile(key, []byte("synthetic upstream credential"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(public, "borrowed-runtime")
	if err := os.Symlink(key, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeMounts(exec.Command("sh"), []string{alias}, auth, state, manager.protectedPaths...); err == nil {
		t.Fatal("a runtime symlink exposed a configured credential file")
	}
}

func TestWorktreeGitScopeRequiresMatchingRegistration(t *testing.T) {
	root := t.TempDir()
	main, worktree := filepath.Join(root, "main"), filepath.Join(root, "worktree")
	if err := os.Mkdir(main, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("prepare synthetic Git repository: %v\n%s", err, output)
		}
	}
	run("init", "--initial-branch=main")
	run("-c", "user.name=Isolation Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "synthetic baseline")
	run("worktree", "add", "-b", "isolated", worktree)
	mounts, err := gitMounts(worktree)
	if err != nil || len(mounts) != 1 || mounts[0].Source != filepath.Join(main, ".git") || !mounts[0].Writable {
		t.Fatalf("registered worktree lost commit access: %v", err)
	}
	foreign := filepath.Join(root, "foreign")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	pointer, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, ".git"), pointer, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitMounts(foreign); err == nil {
		t.Fatal("a forged worktree pointer granted another repository's Git metadata")
	}
}

func TestSupportedManagerRevokesLeaseAndRemovesPrivateConfiguration(t *testing.T) {
	if os.Getenv("WIO_SANDBOX_TEST_REQUIRED") != "1" {
		t.Skip("real supported kernel required; run with WIO_SANDBOX_TEST_REQUIRED=1")
	}
	manager := New(&Config{Enabled: true, Bwrap: os.Getenv("WIO_SANDBOX_TEST_BWRAP")}, "")
	if os.Geteuid() == 0 {
		manager.options.UID, manager.options.GID = 1000, 1000
	}
	ctx, cancel := context.WithTimeout(manager.Context(context.Background()), 30*time.Second)
	defer cancel()
	if status := manager.Status(ctx); status.Mode != "enabled" {
		t.Fatalf("required complete boundary is unavailable: %s", status.Reason)
	}
	for _, provider := range []string{"codex", "claude", "gemini"} {
		t.Run(provider, func(t *testing.T) { testManagerLeaseLifecycle(t, manager, ctx, provider) })
	}
}

func testManagerLeaseLifecycle(t *testing.T, manager *Manager, ctx context.Context, provider string) {
	t.Helper()
	root, err := os.MkdirTemp("", "wio-manager-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	for _, path := range []string{home, workspace} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(root, "outside-credential")
	access := "synthetic-upstream-" + provider
	if err := os.WriteFile(outside, []byte(access), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		presented := req.Header.Get("Authorization")
		if provider == "claude" {
			presented = req.Header.Get("x-api-key")
		} else if provider == "gemini" {
			presented = req.Header.Get("x-goog-api-key")
		}
		if strings.TrimPrefix(presented, "Bearer ") != access {
			t.Error("lease did not use the authentication owned by the trusted Agent")
		}
		w.Write([]byte("{}"))
	}))
	defer upstream.Close()
	env := environment{"HOME": home, "PATH": os.Getenv("PATH"), "WIO_MASTER_KEY": access}
	state := filepath.Join(home, "."+provider)
	switch provider {
	case "codex":
		if err := os.Mkdir(state, 0700); err != nil {
			t.Fatal(err)
		}
		settings := fmt.Sprintf("model_provider = 'test'\n[model_providers.test]\nbase_url = '%s'\nwire_api = 'responses'\n", upstream.URL)
		if err := os.WriteFile(filepath.Join(state, "config.toml"), []byte(settings), 0600); err != nil {
			t.Fatal(err)
		}
		env["WIO_CODEX_API_KEY"] = access
	case "claude":
		env["ANTHROPIC_API_KEY"], env["ANTHROPIC_BASE_URL"] = access, upstream.URL
	case "gemini":
		env["GEMINI_API_KEY"], env["GOOGLE_GEMINI_BASE_URL"] = access, upstream.URL
	}
	if os.Geteuid() == 0 {
		if err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Chown(path, int(manager.options.UID), int(manager.options.GID))
		}); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", `set -eu
test ! -r "$1"
test -z "${WIO_MASTER_KEY-}"
if env | grep -q synthetic-upstream-; then exit 21; fi
if grep -Rq synthetic-upstream- "$HOME"; then exit 22; fi
if [ -n "${CLAUDE_CONFIG_DIR-}" ]; then
  printf '{"hasCompletedOnboarding":true}' > "$HOME/.claude.json.new"
  mv "$HOME/.claude.json.new" "$HOME/.claude.json"
fi
printf 'synthetic result' > manager-result
printf 'manager-boundary-ok'
`, "probe", outside)
	cmd.Env, cmd.Dir = env.entries(), workspace
	scope := childprocess.Codex
	if provider == "claude" {
		scope = childprocess.Claude
	} else if provider == "gemini" {
		scope = childprocess.Gemini
	}
	release, err := childprocess.Prepare(childprocess.WithScope(ctx, scope), cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	public := commandEnvironment(cmd.Env)
	temporary := public["TMPDIR"]
	endpoint, token, route, header := "", public["WIO_CODEX_API_KEY"], "/responses", "Authorization"
	switch provider {
	case "codex":
		config, err := readObject(filepath.Join(temporary, "native-state", "config.toml"), true)
		if err != nil {
			t.Fatal(err)
		}
		endpoint = stringValue(object(object(config["model_providers"])["wio_isolated"])["base_url"])
	case "claude":
		endpoint, token, route, header = public["ANTHROPIC_BASE_URL"], public["ANTHROPIC_API_KEY"], "/v1/messages", "x-api-key"
	case "gemini":
		endpoint, token, route, header = public["GOOGLE_GEMINI_BASE_URL"], public["GEMINI_API_KEY"], "/v1beta/models/test:generateContent", "x-goog-api-key"
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+route, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if header == "Authorization" {
		token = "Bearer " + token
	}
	request.Header.Set(header, token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("temporary lease cannot reach its inference upstream", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("temporary lease was rejected")
	}
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "manager-boundary-ok" {
		t.Fatalf("product manager boundary failed: %v\n%s", err, output)
	}
	if result, err := os.ReadFile(filepath.Join(workspace, "manager-result")); err != nil || string(result) != "synthetic result" {
		t.Fatal("isolated process cannot write its workspace")
	}
	release()
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("process exit leaked private CLI configuration")
	}
	request, _ = http.NewRequest(http.MethodPost, endpoint+route, strings.NewReader("{}"))
	request.Header.Set(header, token)
	client := &http.Client{Timeout: time.Second}
	if response, err := client.Do(request); err == nil {
		response.Body.Close()
		t.Fatal("process exit did not revoke its lease")
	}
}
