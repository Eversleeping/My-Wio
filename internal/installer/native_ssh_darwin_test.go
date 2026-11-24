//go:build darwin && platformintegration

package installer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/hostplatform"
	"github.com/wio-platform/wio/internal/protocol"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

// Opt in only on a disposable administrator-owned CI host. This exercises the
// actual OpenSSH daemon and target installer, with official packages and launchd.
func TestNativeMacSSHInstallRepairAndUninstall(t *testing.T) {
	if os.Getenv("WIO_TEST_MACOS_INSTALL") != "1" {
		t.Skip("requires disposable macOS installation host")
	}
	if !hostplatform.Administrator() {
		t.Fatal("requires root")
	}
	layout := hostplatform.Current()
	for _, name := range []string{layout.State, layout.Binary, launchdFile} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("refusing to replace existing installation: %s", name)
		}
	}
	binary, err := os.ReadFile(os.Getenv("WIO_TEST_AGENT_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	// Hide runner Node installations to require the checksum-verified download branch,
	// then restore every pre-existing file even when the test fails.
	backupNativeNode(t)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if _, err := os.Stat(layout.Config); err == nil {
			_, _ = UninstallLocal(cleanup, "11111111-1111-4111-8111-111111111111")
		} else {
			_ = uninstallPortableLocal(cleanup, layout)
		}
	})
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, hostplatform.Filename("darwin", runtime.GOARCH)), binary, 0600); err != nil {
		t.Fatal(err)
	}
	target, fingerprint := nativeOpenSSHTarget(t, ctx)
	controlURL, latest, enrollments := nativeEnrollmentControl(t)
	service := New(assets)
	probe, err := service.Probe(ctx, HostTarget{Host: target.Host, Port: target.Port})
	if err != nil || probe.Fingerprint != fingerprint {
		t.Fatalf("OpenSSH fingerprint probe failed: %v", err)
	}
	request := InstallRequest{
		OS: "darwin", Target: target, ExpectedFingerprint: fingerprint,
		ControlURL: controlURL, EnrollmentToken: "native-enrollment-token",
		CodexAPIURL: "https://api.example.test/v1", CodexAPIKey: "native-synthetic-key", CodexModel: "test-model",
		GitEndpoint: "https://git.example.test", GitUsername: "native", GitToken: "native-synthetic-token",
		GitCommitName: "Native Test", GitCommitEmail: "native@example.test",
		Versions: CLIVersions{Codex: "0.171.0", Gemini: "0.70.0", Claude: "2.1.330"},
		Progress: func(p InstallProgress) { t.Log("target installer:", p.Step) },
	}
	installed, err := service.Install(ctx, request)
	if err != nil || len(installed.Warnings) != 0 || installed.OS != "darwin" {
		t.Fatalf("SSH installation failed: warnings=%v error=%v", installed.Warnings, err)
	}
	const serverID = "11111111-1111-4111-8111-111111111111"
	if installed.ServerID != serverID || enrollments.Load() != 1 {
		t.Fatal("installation did not enroll exactly once with expected identity")
	}
	if raw, err := exec.CommandContext(ctx, "/usr/local/bin/node", "--version").Output(); err != nil || strings.TrimSpace(string(raw)) != "v22.22.0" {
		t.Fatalf("installer did not provision the expected official Node: %v", err)
	}
	for _, cli := range []struct{ kind, version string }{{"codex", request.Versions.Codex}, {"gemini", request.Versions.Gemini}, {"claude", request.Versions.Claude}} {
		raw, err := os.ReadFile(filepath.Join(layout.State, cli.kind, "current-version"))
		if err != nil || strings.TrimSpace(string(raw)) != cli.version {
			t.Fatalf("%s selection was not verified and persisted: %v", cli.kind, err)
		}
	}
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	for {
		h := latest.Load()
		if h != nil && h.CodexReady && h.GeminiReady && h.ClaudeReady &&
			codexcli.ReportedVersionMatches(h.CodexVersion, request.Versions.Codex) &&
			codexcli.ReportedVersionMatches(h.GeminiVersion, request.Versions.Gemini) &&
			codexcli.ReportedVersionMatches(h.ClaudeVersion, request.Versions.Claude) {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("launchd service did not report all three installed CLIs")
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Log("Real SSH installer downloaded Node, installed all three official CLIs, and launchd published verified CLI inventory")
	before, err := agent.LoadConfig(layout.Config)
	if err != nil {
		t.Fatal(err)
	}
	before.CloneRoot = filepath.Join(layout.State, "custom-projects")
	before.ScanRoots = []string{before.CloneRoot}
	before.DockerPath = "custom-docker"
	before.ExtraEnvironment = []string{"WIO_NATIVE_REPAIR=preserved"}
	if err := agent.SaveConfig(layout.Config, before); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(layout.State, "existing-data")
	if err := os.WriteFile(sentinel, []byte("preserve on repair"), 0600); err != nil {
		t.Fatal(err)
	}
	request.ExpectedServerID, request.PreserveAccessSettings = serverID, true
	repaired, err := service.Install(ctx, request)
	if err != nil || repaired.ServerID != serverID || len(repaired.Warnings) != 0 {
		t.Fatalf("SSH repair failed: warnings=%v error=%v", repaired.Warnings, err)
	}
	after, err := agent.LoadConfig(layout.Config)
	if err != nil {
		t.Fatal(err)
	}
	if after.AgentToken == before.AgentToken || after.AgentToken == "" || enrollments.Load() != 2 {
		t.Fatal("repair did not rotate the token exactly once")
	}
	after.AgentToken = before.AgentToken
	if !reflect.DeepEqual(before, after) {
		t.Fatal("repair changed the server identity or existing runtime settings")
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "preserve on repair" {
		t.Fatal("repair lost existing data")
	}
	t.Log("SSH repair preserved identity, custom runtime settings and existing data while rotating the enrollment token")
	wrong := request
	wrong.ExpectedServerID = "22222222-2222-4222-8222-222222222222"
	if _, err := service.Install(ctx, wrong); err == nil || enrollments.Load() != 2 {
		t.Fatal("wrong-identity repair was not rejected before enrollment")
	}
	uninstall := UninstallRequest{Target: target, ExpectedFingerprint: fingerprint, ExpectedServerID: wrong.ExpectedServerID}
	if _, err := service.Uninstall(ctx, uninstall); err == nil {
		t.Fatal("wrong identity allowed SSH uninstall")
	}
	if _, err := os.Stat(layout.Config); err != nil {
		t.Fatal("rejected uninstall damaged existing installation")
	}
	uninstall.ExpectedServerID = serverID
	if _, err := service.Uninstall(ctx, uninstall); err != nil {
		t.Fatal("SSH uninstall failed:", err)
	}
	for _, name := range []string{layout.State, layout.Binary, launchdFile} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("SSH uninstall left installation file: %s", name)
		}
	}
	t.Log("Real SSH uninstall enforced identity and removed service, executable and private state")
}

func backupNativeNode(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll("/usr/local/lib", 0755); err != nil {
		t.Fatal(err)
	}
	backup, err := os.MkdirTemp("/usr/local/lib", ".wio-native-node-")
	if err != nil {
		t.Fatal(err)
	}
	targets := []string{"/usr/local/bin/node", "/usr/local/bin/npm", "/usr/local/bin/npx", "/usr/local/lib/node_modules"}
	saved := map[string]string{}
	prepared := []string{}
	t.Cleanup(func() {
		for _, name := range prepared {
			if err := os.RemoveAll(name); err != nil {
				t.Error(err)
			}
			if source, ok := saved[name]; ok {
				if err := os.Rename(source, name); err != nil {
					t.Error(err)
				}
			}
		}
		_ = os.Remove(backup)
	})
	for index, name := range targets {
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			prepared = append(prepared, name)
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(backup, strconv.Itoa(index))
		if err := os.Rename(name, dest); err != nil {
			t.Fatal(err)
		}
		saved[name] = dest
		prepared = append(prepared, name)
	}
}

func nativeOpenSSHTarget(t *testing.T, ctx context.Context) (Target, string) {
	t.Helper()
	// OpenSSH StrictModes rejects an authorized_keys ancestry under world-
	// writable /tmp, even when the fixture's own directory is root-owned.
	dir, err := os.MkdirTemp("/var/run", "wio-native-sshd-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	key := func() ([]byte, ssh.PublicKey) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ssh.MarshalPrivateKey(private, "native disposable fixture")
		if err != nil {
			t.Fatal(err)
		}
		signer, err := ssh.NewSignerFromKey(private)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(block), signer.PublicKey()
	}
	host, hostPublic := key()
	login, loginPublic := key()
	if err := os.WriteFile(filepath.Join(dir, "host-key"), host, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), ssh.MarshalAuthorizedKey(loginPublic), 0644); err != nil {
		t.Fatal(err)
	}
	loginUser := os.Getenv("SUDO_USER")
	if loginUser == "" {
		current, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		loginUser = current.Username
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	config := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nAllowUsers %s\nPermitRootLogin yes\nPubkeyAuthentication yes\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nStrictModes yes\nUseDNS no\nSetEnv PATH=/usr/bin:/bin:/usr/sbin:/sbin\n", port, filepath.Join(dir, "host-key"), filepath.Join(dir, "sshd.pid"), filepath.Join(dir, "authorized_keys"), loginUser)
	file := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(file, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "sshd.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "/usr/sbin/sshd", "-D", "-e", "-f", file)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = log.Close()
		if t.Failed() {
			if raw, err := os.ReadFile(log.Name()); err == nil {
				t.Log("Disposable sshd diagnostics:", string(raw))
			}
		}
	})
	target := Target{Host: "127.0.0.1", Port: port, User: loginUser, AuthMethod: "private_key", PrivateKey: string(login)}
	fingerprint := ssh.FingerprintSHA256(hostPublic)
	for i := 0; i < 50; i++ {
		client, _, err := connect(ctx, target, fingerprint)
		if err == nil {
			client.Close()
			return target, fingerprint
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("disposable OpenSSH daemon did not accept fixture authentication")
	return Target{}, ""
}

func nativeEnrollmentControl(t *testing.T) (string, *atomic.Pointer[protocol.Heartbeat], *atomic.Int64) {
	t.Helper()
	control := &nativeAgentControl{heartbeats: make(chan protocol.Heartbeat, 10), connections: make(chan struct{}, 10), commands: make(chan *protocol.ControlEnvelope, 1), results: make(chan protocol.OperationResult, 10)}
	latest := &atomic.Pointer[protocol.Heartbeat]{}
	enrollments := &atomic.Int64{}
	seen := &atomic.Int64{}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case h := <-control.heartbeats:
				latest.Store(&h)
				seen.Store(time.Now().UnixNano())
			case <-done:
				return
			}
		}
	}()
	server := grpc.NewServer(grpc.ForceServerCodec(protocol.Codec()))
	protocol.RegisterAgentServiceServer(server, control)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			server.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/agent/enroll":
			var body struct {
				Token string `json:"enrollment_token"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.Token != "native-enrollment-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			n := enrollments.Add(1)
			seen.Store(0) // Repair must prove a heartbeat from the new service.
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"server_id": "11111111-1111-4111-8111-111111111111", "agent_token": fmt.Sprintf("native-agent-token-%d", n), "scan_roots": []string{hostplatform.Current().State}})
		case "/api/agent/install-status":
			if r.Header.Get("Authorization") != fmt.Sprintf("Bearer native-agent-token-%d", enrollments.Load()) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"server_id": "11111111-1111-4111-8111-111111111111", "online": seen.Load() > time.Now().Add(-30*time.Second).UnixNano()})
		default:
			http.NotFound(w, r)
		}
	})
	httpServer := &http.Server{Handler: h2c.NewHandler(handler, &http2.Server{})}
	go httpServer.Serve(listener)
	t.Cleanup(func() { close(done); server.Stop(); _ = httpServer.Close() })
	return "http://" + listener.Addr().String(), latest, enrollments
}
