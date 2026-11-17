package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/runtimeaccess"
	"github.com/wio-platform/wio/internal/sessionisolation"
)

func TestRuntimeAccessRejectsUnsupportedIsolationBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	c := &Client{config: Config{ServerID: "node"}, seen: map[string]*operationExecution{}, isolation: sessionisolation.New(&sessionisolation.Config{Bwrap: filepath.Join(dir, "missing-bwrap")}, dir)}
	c.runtimeAccessCall = func(context.Context, runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
		t.Fatal("unsupported isolation reached the system settings helper")
		return protocol.AgentAccessSettings{}, nil
	}
	command := protocol.AgentAccessCommand{ServerID: "node", Settings: &protocol.AgentAccessSettings{SessionIsolation: true, Revision: strings.Repeat("a", 64), IsolationSupport: &protocol.AgentIsolationSupport{Supported: true}}}
	raw, _ := json.Marshal(command)
	restart, _, _, err := c.runRuntimeAccess(context.Background(), &protocol.ControlEnvelope{Kind: protocol.OpAgentAccessUpdate, PayloadJSON: raw})
	if err == nil || !strings.Contains(err.Error(), "cannot enable session isolation") || restart != "" || c.agentUpdatePending() {
		t.Fatalf("unsupported isolation was not safely rejected: restart=%q error=%v", restart, err)
	}
}

func TestRuntimeAccessReportsUnsupportedWhileDisabledAndAllowsRecovery(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		dir := t.TempDir()
		config := &sessionisolation.Config{Enabled: enabled, Bwrap: filepath.Join(dir, "missing-bwrap")}
		c := &Client{config: Config{ServerID: "node", SessionIsolation: config}, seen: map[string]*operationExecution{}, isolation: sessionisolation.New(config, dir)}
		c.runtimeAccessCall = func(_ context.Context, request runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
			if enabled && (request.Action != "apply" || request.Settings.SessionIsolation) || !enabled && request.Action != "read" {
				t.Fatal("unexpected configuration mutation")
			}
			return protocol.AgentAccessSettings{Revision: strings.Repeat("a", 64), RestartRequired: enabled}, nil
		}
		kind := protocol.OpAgentAccessRead
		command := protocol.AgentAccessCommand{ServerID: "node"}
		if enabled {
			kind = protocol.OpAgentAccessUpdate
			command.Settings = &protocol.AgentAccessSettings{Revision: strings.Repeat("a", 64)}
		}
		raw, _ := json.Marshal(command)
		restart, data, _, err := c.runRuntimeAccess(context.Background(), &protocol.ControlEnvelope{Kind: kind, PayloadJSON: raw})
		var result protocol.AgentAccessSettings
		if err != nil || json.Unmarshal(data, &result) != nil || result.IsolationSupport == nil || result.IsolationSupport.Supported || result.IsolationSupport.Reason == "" || result.SessionIsolation {
			t.Fatalf("missing support check or blocked recovery: data=%s error=%v", data, err)
		}
		if (restart != "") != enabled {
			t.Fatalf("unexpected recovery restart: %q", restart)
		}
	}
}

func TestRuntimeAccessSavesReceiptBeforeRestartAndReplays(t *testing.T) {
	c := &Client{config: Config{ServerID: "node", StateDir: t.TempDir()}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), seen: map[string]*operationExecution{}, outbound: make(chan *protocol.AgentEnvelope, 8)}
	applied, restarts := 0, 0
	c.runtimeAccessCall = func(_ context.Context, r runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
		if r.Action == "restart" {
			receipt, found, err := c.loadOperationResult("access-change")
			if err != nil || !found || receipt.Status != "succeeded" {
				t.Error("restart preceded durable receipt")
			}
			restarts++
			return protocol.AgentAccessSettings{}, nil
		}
		if r.Action != "apply" {
			t.Errorf("unexpected action %s", r.Action)
		}
		applied++
		return protocol.AgentAccessSettings{AllowSudo: true, Revision: strings.Repeat("b", 64), RestartRequired: true}, nil
	}
	payload, _ := json.Marshal(protocol.AgentAccessCommand{ServerID: "node", Settings: &protocol.AgentAccessSettings{AllowSudo: true, Revision: strings.Repeat("a", 64)}})
	command := &protocol.ControlEnvelope{OperationID: "access-change", Kind: protocol.OpAgentAccessUpdate, PayloadJSON: payload}
	done := make(chan struct{})
	go func() { c.handleOperation(context.Background(), command); close(done) }()
	receiveOperationStarted(t, c.outbound, command.OperationID)
	receipt := receiveAgentEnvelope(t, c.outbound)
	if receipt.Kind != protocol.AgentKindOperationResult {
		t.Fatal("missing receipt")
	}
	c.markSent(receipt)
	<-done
	if applied != 1 || restarts != 1 {
		t.Fatalf("apply=%d restart=%d", applied, restarts)
	}
	c.handleOperation(context.Background(), command)
	_ = receiveAgentEnvelope(t, c.outbound)
	if applied != 1 || restarts != 1 {
		t.Fatal("redelivery repeated a privilege change")
	}
}
func TestRuntimeAccessRejectsWorkIdentityAndConflictingSettingsBeforeHelper(t *testing.T) {
	for _, scenario := range []string{"busy", "identity", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			c := &Client{config: Config{ServerID: "node"}, seen: map[string]*operationExecution{}}
			called := false
			c.runtimeAccessCall = func(context.Context, runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
				called = true
				return protocol.AgentAccessSettings{}, nil
			}
			command := protocol.AgentAccessCommand{ServerID: "node", Settings: &protocol.AgentAccessSettings{AllowSudo: true, Revision: strings.Repeat("a", 64)}}
			if scenario == "busy" {
				c.seen["work"] = &operationExecution{kind: protocol.OpDeployStart, done: make(chan struct{})}
			}
			if scenario == "identity" {
				command.ServerID = "other"
			}
			if scenario == "conflict" {
				command.Settings.SessionIsolation = true
			}
			raw, _ := json.Marshal(command)
			if _, _, _, err := c.runRuntimeAccess(context.Background(), &protocol.ControlEnvelope{Kind: protocol.OpAgentAccessUpdate, PayloadJSON: raw}); err == nil || called {
				t.Fatalf("invalid change reached helper: %v", err)
			}
		})
	}
}
func TestRuntimeAccessFailureReleasesMaintenanceReservation(t *testing.T) {
	c := &Client{config: Config{ServerID: "node"}, seen: map[string]*operationExecution{}}
	c.runtimeAccessCall = func(context.Context, runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
		return protocol.AgentAccessSettings{}, errors.New("synthetic helper failure")
	}
	raw, _ := json.Marshal(protocol.AgentAccessCommand{ServerID: "node", Settings: &protocol.AgentAccessSettings{AllowSudo: true}})
	if _, _, _, err := c.runRuntimeAccess(context.Background(), &protocol.ControlEnvelope{Kind: protocol.OpAgentAccessUpdate, PayloadJSON: raw}); err == nil || c.agentUpdatePending() {
		t.Fatal("failed change kept Agent locked")
	}
}

func TestRuntimeAccessReadNeverRestartsPendingChange(t *testing.T) {
	c := &Client{config: Config{ServerID: "node"}, seen: map[string]*operationExecution{}}
	c.runtimeAccessCall = func(_ context.Context, r runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
		if r.Action != "read" {
			t.Fatalf("read initiated %s", r.Action)
		}
		return protocol.AgentAccessSettings{Revision: strings.Repeat("a", 64), RestartRequired: true}, nil
	}
	raw, _ := json.Marshal(protocol.AgentAccessCommand{ServerID: "node"})
	restart, data, _, err := c.runRuntimeAccess(context.Background(), &protocol.ControlEnvelope{Kind: protocol.OpAgentAccessRead, PayloadJSON: raw})
	var settings protocol.AgentAccessSettings
	if err != nil || restart != "" || json.Unmarshal(data, &settings) != nil || !settings.RestartRequired || c.agentUpdatePending() {
		t.Fatalf("unsafe pending read: restart=%q data=%s error=%v", restart, data, err)
	}
}

func TestRuntimeAccessExplicitRestartPersistsReceiptAndReplaysWithoutRestartingAgain(t *testing.T) {
	c := &Client{config: Config{ServerID: "node", StateDir: t.TempDir()}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), seen: map[string]*operationExecution{}, outbound: make(chan *protocol.AgentEnvelope, 8)}
	reads, restarts := 0, 0
	c.runtimeAccessCall = func(_ context.Context, r runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
		if r.Action == "restart" {
			receipt, found, err := c.loadOperationResult("restart-only")
			if !r.Force || !found || err != nil || receipt.Status != "succeeded" {
				t.Error("explicit restart preceded durable receipt or lost its intent")
			}
			restarts++
		} else if r.Action == "read" {
			reads++
		} else {
			t.Error("explicit restart attempted to change settings")
		}
		return protocol.AgentAccessSettings{Revision: strings.Repeat("a", 64)}, nil
	}
	raw, _ := json.Marshal(protocol.AgentAccessCommand{ServerID: "node"})
	command := &protocol.ControlEnvelope{OperationID: "restart-only", Kind: protocol.OpAgentRestart, PayloadJSON: raw}
	done := make(chan struct{})
	go func() { c.handleOperation(context.Background(), command); close(done) }()
	receiveOperationStarted(t, c.outbound, command.OperationID)
	receipt := receiveAgentEnvelope(t, c.outbound)
	var result protocol.OperationResult
	var settings protocol.AgentAccessSettings
	if json.Unmarshal(receipt.PayloadJSON, &result) != nil || json.Unmarshal(result.Data, &settings) != nil || settings.InstanceID == "" {
		t.Fatal("restart receipt must identify the current process instance")
	}
	c.markSent(receipt)
	<-done
	c.handleOperation(context.Background(), command)
	_ = receiveAgentEnvelope(t, c.outbound)
	other := &Client{}
	if reads != 1 || restarts != 1 || other.runtimeInstanceID() == settings.InstanceID || c.runtimeInstanceID() != settings.InstanceID {
		t.Fatalf("restart/replay identity contract broken: reads=%d restarts=%d", reads, restarts)
	}
}

func TestRuntimeAccessDockerUsesLocalHostProcessIdentity(t *testing.T) {
	dir := t.TempDir()
	arguments := filepath.Join(dir, "arguments")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nif [ \"$3\" = image ]; then printf 'sha256:" + strings.Repeat("a", 64) + "\\n'; exit 0; fi\nprintf '%s\\n' \"$@\" > '" + arguments + "'\ncat >/dev/null\nprintf '%s\\n' '{\"settings\":{\"revision\":\"" + strings.Repeat("b", 64) + "\"}}'\n"
	if err := os.WriteFile(docker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "old-helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var request struct{ Action string }
		if err := json.NewDecoder(connection).Decode(&request); err != nil || request.Action != runtimeaccess.HelperAction {
			t.Error("Agent did not require the recovery-capable helper")
		}
		_ = json.NewEncoder(connection).Encode(map[string]string{"error": "unsupported prerequisite helper action"})
	}()
	c := &Client{config: Config{configPath: "/etc/wio-agent/config.json", DockerPath: docker, PrerequisiteSocket: socket}}
	if _, err := c.callRuntimeAccess(context.Background(), runtimeaccess.Request{Action: "read", ServerID: "node"}); err != nil {
		t.Fatal(err)
	}
	<-done
	raw, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	args := "\n" + string(raw)
	for _, contract := range []string{"\n--host\nunix:///var/run/docker.sock\n", "\n--pid\nhost\n", "\n--network\nnone\n", "\n--cap-drop\nALL\n", "\ntype=bind,src=/,dst=/host,readonly\n"} {
		if !strings.Contains(args, contract) {
			t.Fatalf("local system helper contract missing %q", contract)
		}
	}
	if strings.Contains(args, "\n--privileged\n") {
		t.Fatal("system helper must keep its bounded capabilities")
	}
}
