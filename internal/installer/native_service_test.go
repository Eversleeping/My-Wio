//go:build (darwin || windows) && platformintegration

package installer

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/hostplatform"
	"github.com/wio-platform/wio/internal/protocol"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

type nativeAgentControl struct {
	heartbeats  chan protocol.Heartbeat
	connections chan struct{}
	commands    chan *protocol.ControlEnvelope
	results     chan protocol.OperationResult
}

func startNativeControl(listener net.Listener, control *nativeAgentControl, gitArchive string) func() {
	server := grpc.NewServer(grpc.ForceServerCodec(protocol.Codec()))
	protocol.RegisterAgentServiceServer(server, control)
	var connections sync.Map
	httpServer := &http.Server{Handler: h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			server.ServeHTTP(w, r)
			return
		}
		if gitArchive != "" && r.URL.Path == "/api/agent/install-tools/windows-git" && r.URL.Query().Get("sha256") == WindowsGitSHA256 {
			http.ServeFile(w, r, gitArchive)
			return
		}
		http.NotFound(w, r)
	}), &http2.Server{}), ConnState: func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Store(conn, true)
		} else if state == http.StateClosed {
			connections.Delete(conn)
		}
	}}
	go httpServer.Serve(listener)
	return func() {
		// h2c hijacks connections; close those too, as replacing the process does.
		connections.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
		_ = httpServer.Close()
		server.Stop()
	}
}

func (s *nativeAgentControl) Connect(stream protocol.AgentServiceConnectServer) error {
	select {
	case s.connections <- struct{}{}:
	default:
	}
	if err := stream.Send(&protocol.ControlEnvelope{Kind: protocol.ControlKindKeepalive, PayloadJSON: json.RawMessage(`{}`)}); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case command := <-s.commands:
				if stream.Send(command) != nil {
					return
				}
			}
		}
	}()
	for {
		message, err := stream.Recv()
		if err != nil {
			return err
		}
		if message.Kind == protocol.AgentKindHeartbeat {
			var h protocol.Heartbeat
			if json.Unmarshal(message.PayloadJSON, &h) == nil {
				select {
				case s.heartbeats <- h:
				default:
				}
			}
		}
		if message.Kind == protocol.AgentKindOperationResult {
			var result protocol.OperationResult
			if json.Unmarshal(message.PayloadJSON, &result) == nil {
				select {
				case s.results <- result:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

// This test is opt-in and only runs on disposable native CI machines. It uses
// a loopback gRPC control plane, real dedicated accounts, and the real service
// manager. It never downloads AI models or sends production credentials.
func TestNativeServiceHeartbeatRestartAndUninstall(t *testing.T) {
	if !hostplatform.Administrator() {
		t.Fatal("native service integration requires administrator/root")
	}
	layout := hostplatform.Current()
	if _, err := os.Lstat(layout.State); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refusing to replace an existing Wio directory")
	}
	source := os.Getenv("WIO_TEST_AGENT_BINARY")
	if source == "" {
		t.Fatal("WIO_TEST_AGENT_BINARY must name the freshly built native Agent")
	}
	binary, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	gitArchive := nativeGitArchive(t)
	// The first Windows boot now downloads and extracts a full Git distribution.
	// Keep restart/uninstall coverage inside a budget which includes that work.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	control := &nativeAgentControl{heartbeats: make(chan protocol.Heartbeat, 10), connections: make(chan struct{}, 10), commands: make(chan *protocol.ControlEnvelope, 1), results: make(chan protocol.OperationResult, 10)}
	address := listener.Addr().String()
	stopControl := startNativeControl(listener, control, gitArchive)
	defer func() { stopControl() }()
	if err := preparePortableService(ctx, layout); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if t.Failed() {
			nativeServiceDiagnostics(t, layout)
		}
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		_ = uninstallPortableLocal(cleanup, layout)
	}()
	for _, dir := range []string{layout.State} {
		if err := managedDirectory(dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareBinaryDirectory(filepath.Dir(layout.Binary)); err != nil {
		t.Fatal(err)
	}
	if err := atomicPortableWrite(layout.Binary, binary, 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicPortableWrite(layout.Key, []byte("native-test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	probe, verifyDependency := nativeDependencyProbe(t, layout, binary)
	id := "11111111-1111-4111-8111-111111111111"
	config := agent.Config{ControlURL: "http://" + listener.Addr().String(), ServerID: id, AgentToken: "native-test-token", StateDir: layout.State, CloneRoot: filepath.Join(layout.State, "projects"), ScanRoots: []string{layout.State}, CodexPath: probe}
	if err := agent.SaveConfig(layout.Config, config); err != nil {
		t.Fatal(err)
	}
	if err := protectPortableState(ctx, layout, layout.State); err != nil {
		t.Fatal(err)
	}
	if err := startPortableService(ctx, layout); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-control.heartbeats:
		if verifyDependency && (!h.CodexReady || h.CodexVersion != buildinfo.Version) {
			t.Fatalf("service could not execute normalized dependency: ready=%v version=%q", h.CodexReady, h.CodexVersion)
		}
		if h.OS != runtime.GOOS || h.Architecture != runtime.GOARCH {
			t.Fatalf("wrong platform: %+v", h)
		}
		if slices.Contains(h.Capabilities, protocol.CapabilityAgentAccess) || slices.Contains(h.Capabilities, protocol.CapabilityDeployRollbackExact) {
			t.Fatal("Linux-only capability advertised")
		}
	case <-ctx.Done():
		t.Fatal("native service did not send a heartbeat")
	}
	select {
	case <-control.connections:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Exercise the credential operations under the actual service account/ACL,
	// not only the administrator which installed the service. Values are fixtures.
	for _, update := range []struct {
		kind string
		body any
	}{
		{protocol.OpCodexCredentialsConfigure, protocol.ConfigureCredentialsCommand{Scope: "codex", CodexAPIURL: "https://api.example.test/v1", CodexAPIKey: "native-synthetic-key", CodexModel: "test-model"}},
		{protocol.OpGitCredentialsConfigure, protocol.ConfigureCredentialsCommand{Scope: "git", GitEndpoint: "https://git.example.test", GitUsername: "native-test", GitToken: "native-synthetic-token", GitCommitName: "Native Test", GitCommitEmail: "native@example.test"}},
		{protocol.OpGeminiCredentialsConfigure, protocol.ConfigureGeminiCredentialsCommand{Endpoint: "https://gemini.example.test", APIKey: "native-synthetic-key", Model: "test-model"}},
		{protocol.OpClaudeCredentialsConfigure, protocol.ConfigureClaudeCredentialsCommand{Endpoint: "https://claude.example.test", APIKey: "native-synthetic-key", Model: "test-model"}},
	} {
		for attempt := 0; attempt < 2; attempt++ {
			// A second application must replace the existing files successfully.
			raw, _ := json.Marshal(update.body)
			opID := update.kind + time.Now().Format("150405.000000000")
			control.commands <- &protocol.ControlEnvelope{OperationID: opID, Kind: update.kind, PayloadJSON: raw}
			select {
			case result := <-control.results:
				if result.OperationID != opID || result.Status != "succeeded" {
					t.Fatalf("service credential operation %s failed: %s", update.kind, result.Message)
				}
			case <-ctx.Done():
				t.Fatal("service did not acknowledge credential operation: " + update.kind)
			}
		}
	}
	for _, file := range []string{"codex.key", ".git-credentials", ".gitconfig", filepath.Join("gemini", "api.json"), filepath.Join("claude", "api.json")} {
		if info, err := os.Stat(filepath.Join(layout.State, file)); err != nil || info.Size() == 0 {
			t.Fatalf("service credential file %s missing: %v", file, err)
		}
	}
	t.Log("All four credential kinds applied and replaced under the native service account")
	project, _ := json.Marshal(protocol.GitProjectCreateCommand{ProjectID: "native-git-project", Name: "native-project", InitialBranch: "main", InitializeREADME: true})
	control.commands <- &protocol.ControlEnvelope{OperationID: "native-create-project", Kind: protocol.OpGitProjectCreate, PayloadJSON: project}
	select {
	case result := <-control.results:
		if result.OperationID != "native-create-project" || result.Status != "succeeded" {
			t.Fatalf("service could not create Git project: %s", result.Message)
		}
		var created protocol.GitProjectCreateResult
		if json.Unmarshal(result.Data, &created) != nil || created.Branch != "main" || created.CommitSHA == "" {
			t.Fatal("service did not commit README on the requested branch")
		}
	case <-ctx.Done():
		t.Fatal("service did not acknowledge Git project creation")
	}
	if _, err := os.Stat(filepath.Join(layout.State, "projects", "native-project", ".git")); err != nil {
		t.Fatal("service did not create the repository", err)
	}
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(filepath.Join(layout.State, "tools", "git", "cmd", "git.exe")); err != nil {
			t.Fatal("worker did not provision its own Git", err)
		}
		t.Log("Worker downloaded and verified private Git from the control plane")
	}
	t.Log("Native service created a project with README")
	stopControl()
	draining := true
	for draining {
		select {
		case <-control.heartbeats:
		default:
			draining = false
		}
	}
	listener, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	stopControl = startNativeControl(listener, control, gitArchive)
	select {
	case <-control.connections:
	case <-ctx.Done():
		t.Fatal("service did not reconnect after control-plane replacement")
	}
	select {
	case <-control.heartbeats:
	case <-ctx.Done():
		t.Fatal("service did not resume heartbeats after control-plane replacement")
	}
	t.Log("Native service reconnected and resumed heartbeats after control-plane replacement without restarting the service")
	payload, _ := json.Marshal(protocol.AgentAccessCommand{ServerID: id})
	control.commands <- &protocol.ControlEnvelope{OperationID: "native-restart", Kind: protocol.OpAgentRestart, PayloadJSON: payload}
	select {
	case <-control.connections:
	case <-ctx.Done():
		t.Fatal("native service did not reconnect after restart")
	}
	if _, err := UninstallLocal(ctx, "22222222-2222-4222-8222-222222222222"); !errors.Is(err, ErrServerMismatch) {
		t.Fatalf("wrong identity allowed uninstall: %v", err)
	}
	if _, err := UninstallLocal(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(layout.Config); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("uninstall left managed credentials")
	}
}
