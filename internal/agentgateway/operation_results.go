package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
	"time"
)

// handleOperationResult applies an Agent result. outcomeUnknown marks results
// synthesized for operations the Agent lost track of: their side effects on
// disk may be partial, so resources are not reported as cleanly unchanged.
func (g *Gateway) handleOperationResult(ctx context.Context, serverID string, result protocol.OperationResult, outcomeUnknown bool) error {
	operation, err := g.store.Operation(ctx, result.OperationID)
	if err != nil {
		return err
	}
	if operation.ServerID != serverID {
		return errors.New("operation result server mismatch")
	}
	if operation.Kind == protocol.OpTaskArtifactCapture || operation.Kind == protocol.OpTaskArtifactHandoff {
		id, err := g.store.CompleteTaskArtifact(ctx, operation, result, outcomeUnknown)
		if errors.Is(err, store.ErrOperationNotActive) {
			return nil
		}
		if err != nil {
			return err
		}
		if id != "" {
			g.hub.Publish(protocol.StreamEvent{StreamID: "task:" + id, Kind: "task.artifact.updated", OccurredAt: time.Now().UTC()})
		}
		return nil
	}
	if err := g.store.RecordTaskOperationResult(ctx, serverID, result, outcomeUnknown); err != nil {
		return err
	}
	// A queued or in-flight operation may be cancelled from the UI. Ignore a
	// late Agent result so it cannot resurrect a cancelled turn or overwrite
	// its snapshot with stale data.
	if operation.Status != "queued" && operation.Status != "delivered" && operation.Status != "running" {
		return g.reconcileCompletedTurn(ctx, operation)
	}
	if result.Status == "cancelled" && protocol.CanInterruptOperation(operation.Kind) {
		completed, err := g.store.CompleteInterruptedOperation(ctx, operation, result)
		if err != nil || !completed {
			return err
		}
		return publishOperationResult(ctx, g, serverID, result)
	}
	geminiTurn := operation.Kind == protocol.OpGeminiTurnStart || operation.Kind == protocol.OpGeminiTurnRewrite || operation.Kind == protocol.OpClaudeTurnStart
	geminiInterrupted := geminiTurn && (result.Status == "interrupted" || result.Status == "cancelled" || (result.Status == "failed" && result.Message == context.Canceled.Error()))
	if geminiInterrupted {
		// Agent operations use the shared cancelled state for metrics and
		// filtering, while the Gemini session displays interrupted.
		result.Status = "cancelled"
	}
	resultLogFields := []any{
		"server_id", serverID,
		"operation_id", operation.ID,
		"kind", operation.Kind,
		"status", result.Status,
		"total_elapsed_ms", time.Since(operation.CreatedAt).Milliseconds(),
	}
	if operation.DeliveredAt != nil {
		resultLogFields = append(resultLogFields, "execution_elapsed_ms", time.Since(*operation.DeliveredAt).Milliseconds())
	}
	g.evaluateOperationLatency(ctx, serverID, operation)
	g.log.Info("Agent operation result received", resultLogFields...)
	if handled, err := g.completeOperationResources(ctx, serverID, operation, &result, outcomeUnknown); handled || err != nil {
		return err
	}
	geminiCompletionClaimed := false
	if operation.Kind == protocol.OpGeminiCredentialsConfigure {
		err = g.store.CompleteGeminiCredentialUpdate(ctx, result)
	} else if operation.Kind == protocol.OpClaudeCredentialsConfigure {
		err = g.store.CompleteClaudeCredentialUpdate(ctx, result)
	} else if operation.Kind == protocol.OpCredentialsConfigure || operation.Kind == protocol.OpCodexCredentialsConfigure || operation.Kind == protocol.OpGitCredentialsConfigure {
		err = g.store.CompleteCredentialUpdate(ctx, result)
	} else if geminiInterrupted || operation.Kind == protocol.OpClaudeTurnStart {
		geminiCompletionClaimed, err = g.store.CompleteOperationIfActive(ctx, result)
	} else {
		err = g.store.CompleteOperation(ctx, result)
	}
	if err != nil {
		return err
	}
	if (geminiInterrupted || operation.Kind == protocol.OpClaudeTurnStart) && !geminiCompletionClaimed {
		return nil
	}
	if operation.Kind == protocol.OpClaudeThreadFork || operation.Kind == protocol.OpGitWorktreeCreate {
		if err := g.completeClaudeForkSource(ctx, operation); err != nil {
			return err
		}
	}
	if operation.Kind == protocol.OpGeminiThreadFork || operation.Kind == protocol.OpGitWorktreeCreate {
		if err := g.completeGeminiForkSource(ctx, operation); err != nil {
			return err
		}
	}
	updatedOperation, statusErr := g.store.Operation(ctx, result.OperationID)
	if statusErr != nil {
		return statusErr
	}
	if updatedOperation.Status == "cancelled" && !geminiCompletionClaimed {
		return nil
	}
	if operation.Kind == protocol.OpGeminiTurnStart || operation.Kind == protocol.OpGeminiTurnRewrite || operation.Kind == protocol.OpGeminiTurnInterrupt || operation.Kind == protocol.OpGeminiTurnSteer {
		if err := g.completeGeminiOperation(ctx, operation, result); err != nil {
			return err
		}
	}
	if operation.Kind == protocol.OpClaudeTurnStart || operation.Kind == protocol.OpClaudeTurnInterrupt || operation.Kind == protocol.OpClaudeTurnSteer || operation.Kind == protocol.OpClaudeQuestionAnswer {
		if err := g.completeClaudeOperation(ctx, operation, result); err != nil {
			return err
		}
	}
	if operation.Kind == protocol.OpCodexTurnStart || operation.Kind == protocol.OpCodexTurnRewrite {
		if result.Status == "succeeded" {
			_ = g.store.MarkScheduledTaskByOperation(ctx, result.OperationID, "running", "")
		}
	}
	if (operation.Kind == protocol.OpCodexTurnStart || operation.Kind == protocol.OpCodexTurnRewrite) && result.Status == "failed" {
		if err := g.failCodexTurn(ctx, operation, result); err != nil {
			return err
		}
	}
	if (operation.Kind == protocol.OpCodexTurnInterrupt || operation.Kind == protocol.OpCodexTurnSteer || operation.Kind == protocol.OpCodexApproval || operation.Kind == protocol.OpCodexThreadCompact) && result.Status == "failed" {
		if err := g.failCodexControlOperation(ctx, operation, result); err != nil {
			return err
		}
	}
	// Image bytes are fetched through the authenticated preview APIs. Broadcasting
	// them to every connected console duplicates multi-megabyte payloads.
	eventResult := result
	if operation.Kind == protocol.OpWorkspaceImagePreview || operation.Kind == protocol.OpWorkspaceFilePreview {
		eventResult.Data = nil
	}
	payload, err := json.Marshal(eventResult)
	if err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{StreamID: serverID, Kind: protocol.EventOperationPrefix + result.Status, Payload: security.RedactJSON(payload)})
}

// A completion can commit its operation before a later session update fails.
// Repair only the still-bound active turn, using the stored result rather than
// the late Agent payload. In particular, a UI-cancelled session stays stopped.
func (g *Gateway) reconcileCompletedTurn(ctx context.Context, operation store.Operation) error {
	if operation.Status != "succeeded" && operation.Status != "failed" && operation.Status != "cancelled" {
		return nil
	}
	engine := ""
	switch operation.Kind {
	case protocol.OpClaudeTurnInterrupt, protocol.OpGeminiTurnInterrupt:
		if operation.Status != "succeeded" {
			return nil
		}
		result := protocol.OperationResult{OperationID: operation.ID, Status: operation.Status, Message: operation.Result, Data: json.RawMessage(operation.ResultData)}
		if operation.Kind == protocol.OpClaudeTurnInterrupt {
			return g.completeClaudeOperation(ctx, operation, result)
		}
		return g.completeGeminiOperation(ctx, operation, result)
	case protocol.OpClaudeTurnStart:
		engine = "claude"
	case protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite:
		engine = "gemini"
	case protocol.OpCodexTurnStart, protocol.OpCodexTurnRewrite:
		if operation.Status != "failed" {
			return nil // A successful Codex start only acknowledges acceptance.
		}
		engine = "codex"
	default:
		return nil
	}
	var command struct {
		ThreadID string `json:"thread_id"`
		Start    struct {
			ThreadID string `json:"thread_id"`
		} `json:"start"`
	}
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	threadID := command.ThreadID
	if threadID == "" {
		threadID = command.Start.ThreadID
	}
	var active int
	query := "SELECT COUNT(*) FROM " + engine + "_threads WHERE id=? AND status IN ('queued','running','interrupting')"
	args := []any{threadID}
	if engine != "codex" {
		query += " AND operation_id=?"
		args = append(args, operation.ID)
	}
	if err := g.store.DB.GetContext(ctx, &active, g.store.Q(query), args...); err != nil {
		return err
	}
	if active == 0 {
		return nil
	}
	result := protocol.OperationResult{OperationID: operation.ID, Status: operation.Status, Message: operation.Result, Data: json.RawMessage(operation.ResultData)}
	switch engine {
	case "claude":
		return g.completeClaudeOperation(ctx, operation, result)
	case "gemini":
		return g.completeGeminiOperation(ctx, operation, result)
	default:
		return g.failCodexTurn(ctx, operation, result)
	}
}

// A lost operation may have stopped half-way through moving, cloning or
// creating files; keep the resource retryable instead of claiming it is clean.
func uncertainStatus(outcomeUnknown bool, status string) string {
	if outcomeUnknown {
		return "partial"
	}
	return status
}

// reconcileOperation handles the Agent's answer to a keepalive reconcile
// request. Only an explicit "unknown" fails an operation: old Agents never
// answer, and an active long-running deploy or turn answers "active".
func (g *Gateway) reconcileOperation(ctx context.Context, serverID string, reconcile protocol.OperationReconcile) error {
	if reconcile.State != protocol.OperationReconcileUnknown {
		return nil
	}
	operation, err := g.store.Operation(ctx, reconcile.OperationID)
	if err != nil {
		return err
	}
	if operation.ServerID != serverID {
		return errors.New("operation reconcile server mismatch")
	}
	// Only running operations were asked about; a queued or delivered row is
	// being re-sent and a finished one already has its result.
	if operation.Status != "running" || !store.ReconcilesOperationKind(operation.Kind) {
		return nil
	}
	g.log.Warn("Agent lost track of running operation", "server_id", serverID, "operation_id", operation.ID, "kind", operation.Kind)
	if err := g.failLostDeployment(ctx, operation); err != nil {
		return err
	}
	return g.handleOperationResult(ctx, serverID, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: protocol.OperationLostMessage}, true)
}

func publishOperationResult(ctx context.Context, g *Gateway, serverID string, result protocol.OperationResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{StreamID: serverID, Kind: protocol.EventOperationPrefix + result.Status, Payload: security.RedactJSON(payload)})
}
