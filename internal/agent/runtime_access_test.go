package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/runtimeaccess"
)

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
