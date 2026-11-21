//go:build (darwin || windows) && platformintegration

package installer

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/hostplatform"
	"github.com/wio-platform/wio/internal/protocol"
	"google.golang.org/grpc"
)

type nativeAgentControl struct {
	heartbeats  chan protocol.Heartbeat
	connections chan struct{}
	commands    chan *protocol.ControlEnvelope
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	control := &nativeAgentControl{heartbeats: make(chan protocol.Heartbeat, 10), connections: make(chan struct{}, 10), commands: make(chan *protocol.ControlEnvelope, 1)}
	server := grpc.NewServer(grpc.ForceServerCodec(protocol.Codec()))
	protocol.RegisterAgentServiceServer(server, control)
	go server.Serve(listener)
	defer server.Stop()
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
