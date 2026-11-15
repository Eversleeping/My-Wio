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
)

// Malformed commands must fail before adapters, credentials or the filesystem
// are touched, including when the result is replayed after redelivery.
func TestOperationDispatchRejectsMalformedCommands(t *testing.T) {
	kinds := []string{
		protocol.OpOperationInterrupt, protocol.OpAgentUpdate,
		protocol.OpGeminiTurnInterrupt, protocol.OpClaudeTurnSteer, protocol.OpClaudeTurnInterrupt,
		protocol.OpGeminiSkillsManage, protocol.OpClaudeSkillsList, protocol.OpGeminiSkillsList,
		protocol.OpWorkspaceFiles, protocol.OpWorkspaceFileWrite, protocol.OpWorkspaceFilePreview,
		protocol.OpWorkspaceImagePreview, protocol.OpWorkspaceChanges, protocol.OpWorkspaceDiffPreview,
		protocol.OpCodexGoalGet, protocol.OpCodexMCPList, protocol.OpCodexSkillsList,
		protocol.OpCodexModelsList, protocol.OpCodexStatusSnapshot, protocol.OpCodexThreadCompact,
		protocol.OpCodexThreadFork, protocol.OpGitProjectCreate, protocol.OpGitProjectDelete,
		protocol.OpGitWorkspaceInspect, protocol.OpGitWorkspaceWrite, protocol.OpGitWorkspaceLifecycle,
		protocol.OpGitWorkspaceClone, protocol.OpClaudeThreadFork, protocol.OpGeminiThreadFork,
		protocol.OpGitWorktreeCreate, protocol.OpGitWorktreeCleanup, protocol.OpDeployContainer,
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			client := &Client{log: slog.New(slog.NewTextHandler(io.Discard, nil)), outbound: make(chan *protocol.AgentEnvelope, 2), seen: make(map[string]*operationExecution)}
			envelope := &protocol.ControlEnvelope{OperationID: "malformed", Kind: kind, PayloadJSON: json.RawMessage(`{`)}
			client.handleOperation(context.Background(), envelope)
			receiveOperationStarted(t, client.outbound, envelope.OperationID)
			first := receiveAgentEnvelope(t, client.outbound)
			var result protocol.OperationResult
			if err := json.Unmarshal(first.PayloadJSON, &result); err != nil {
				t.Fatal(err)
			}
			if first.Kind != protocol.AgentKindOperationResult || result.Status != "failed" || !strings.Contains(result.Message, "unexpected end of JSON input") || len(result.Data) != 0 {
				t.Fatalf("unexpected malformed-command receipt: %#v", result)
			}
			client.handleOperation(context.Background(), envelope)
			replay := receiveAgentEnvelope(t, client.outbound)
			if replay.Kind != first.Kind || string(replay.PayloadJSON) != string(first.PayloadJSON) {
				t.Fatalf("redelivery changed the failed receipt: %s", replay.PayloadJSON)
			}
		})
	}
}

func TestExecuteRejectsMalformedCommandsBeforeSideEffects(t *testing.T) {
	kinds := []string{
		protocol.OpGitImport, protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite, protocol.OpClaudeTurnStart,
		protocol.OpClaudeQuestionAnswer, protocol.OpGeminiQuestionAnswer,
		protocol.OpGeminiTurnInterrupt, protocol.OpGeminiTurnSteer,
		protocol.OpCodexTurnStart, protocol.OpCodexTurnRewrite, protocol.OpCodexTurnSteer,
		protocol.OpCodexTurnInterrupt, protocol.OpCodexApproval,
		protocol.OpGeminiCredentialsConfigure, protocol.OpClaudeCredentialsConfigure,
		protocol.OpCredentialsConfigure, protocol.OpCodexCredentialsConfigure, protocol.OpGitCredentialsConfigure,
		protocol.OpCodexUpdate, protocol.OpCLIInstall, protocol.OpDeployStart, protocol.OpDeployRollback,
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			client := &Client{}
			err := client.execute(context.Background(), &protocol.ControlEnvelope{OperationID: "malformed", Kind: kind, PayloadJSON: json.RawMessage(`{`)})
			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) {
				t.Fatalf("expected JSON rejection before execution, got %v", err)
			}
		})
	}
}

func TestInventoryOperationPublishesInventory(t *testing.T) {
	client := &Client{config: Config{ScanRoots: []string{t.TempDir()}}, outbound: make(chan *protocol.AgentEnvelope, 1)}
	if err := client.execute(context.Background(), &protocol.ControlEnvelope{Kind: protocol.OpInventoryScan}); err != nil {
		t.Fatal(err)
	}
	if envelope := receiveAgentEnvelope(t, client.outbound); envelope.Kind != protocol.AgentKindInventory {
		t.Fatalf("expected inventory, got %q", envelope.Kind)
	}
}

func TestOperationWithoutIDIsIgnored(t *testing.T) {
	client := &Client{outbound: make(chan *protocol.AgentEnvelope, 1)}
	client.handleOperation(context.Background(), &protocol.ControlEnvelope{Kind: protocol.OpInventoryScan})
	if len(client.outbound) != 0 {
		t.Fatal("an operation without an ID was accepted")
	}
}
