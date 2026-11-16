package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/deployer"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

func TestClaudeOwnershipProducerReportsActiveExecution(t *testing.T) {
	client := outboundTestClient(1)
	client.seen["active"] = &operationExecution{kind: protocol.OpClaudeTurnStart, done: make(chan struct{})}
	client.reconcileClaudeOperations(json.RawMessage(`{"claude_operation_ids":["active"]}`))
	envelope := receiveAgentEnvelope(t, client.outbound)
	if envelope.Kind != protocol.AgentKindClaudeOwnershipActive || string(envelope.PayloadJSON) != `{"operation_id":"active"}` {
		t.Fatalf("active ownership changed: %+v", envelope)
	}
}

func TestSessionToolProducerPreservesCallAndMatchesReply(t *testing.T) {
	client := outboundTestClient(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ref := sessiontools.Ref{Engine: "claude", ID: "source-thread"}
	finished := make(chan error, 1)
	go func() {
		reply, err := client.callSessionTool(ctx, ref, "source-operation", "list_sessions", json.RawMessage(`{"request_id":"request","limit":5}`))
		if err == nil && reply.RequestID != "request" {
			finished <- context.Canceled
			return
		}
		finished <- err
	}()
	envelope := receiveAgentEnvelope(t, client.outbound)
	var call sessiontools.Call
	if err := json.Unmarshal(envelope.PayloadJSON, &call); err != nil {
		t.Fatal(err)
	}
	if envelope.Kind != protocol.AgentKindSessionToolRequest || call.ID == "" || call.ID != envelope.MessageID || call.Source != ref || call.OperationID != "source-operation" || call.Name != "list_sessions" || string(call.Arguments) != `{"request_id":"request","limit":5}` {
		t.Fatalf("tool call changed: %+v %+v", envelope, call)
	}
	raw, err := json.Marshal(sessiontools.Reply{ID: call.ID, RequestID: "request", Status: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	client.receiveSessionToolReply(raw)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	client.toolMu.Lock()
	defer client.toolMu.Unlock()
	if len(client.toolReplies) != 0 {
		t.Fatal("completed tool waiter retained")
	}
}

func TestDeploymentProducerPublishesFailureStatus(t *testing.T) {
	client := outboundTestClient(8)
	client.deployer = deployer.New("unavailable-fixture-docker")
	err := client.runDeployment(context.Background(), protocol.DeployCommand{DeploymentID: "deployment", TargetID: "target", SourceType: "git", Repository: "", CommitRef: "main", ReleaseRoot: t.TempDir()})
	if err == nil {
		t.Fatal("invalid deployment succeeded")
	}
	found := false
	for len(client.outbound) > 0 {
		envelope := receiveAgentEnvelope(t, client.outbound)
		if envelope.Kind != protocol.AgentKindDeploymentStatus {
			t.Fatalf("unexpected envelope: %s", envelope.Kind)
		}
		var status protocol.DeploymentStatus
		if err := json.Unmarshal(envelope.PayloadJSON, &status); err != nil {
			t.Fatal(err)
		}
		if status.DeploymentID != "deployment" {
			t.Fatalf("lost deployment identity: %+v", status)
		}
		if status.Status == "failed" && status.Message != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("deployment failed without failure status")
	}
}
