package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/wio-platform/wio/internal/gitrepository"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
)

func (c *Client) runTaskArtifactCapture(ctx context.Context, op *protocol.ControlEnvelope) (string, json.RawMessage, bool, error) {
	var input protocol.TaskArtifactCapture
	if err := decodeOperationCommand(op.PayloadJSON, &input); err != nil {
		return "", nil, false, err
	}
	root, err := scanner.ResolveWorkspaceRoot(input.Workspace, c.inventoryRoots())
	if err != nil {
		return "", nil, false, err
	}
	manifest, err := gitrepository.CaptureTaskArtifact(ctx, root, input.Paths)
	if err != nil {
		return "", nil, false, err
	}
	raw, err := json.Marshal(manifest)
	return "", raw, false, err
}

func (c *Client) runTaskArtifactHandoff(ctx context.Context, op *protocol.ControlEnvelope) (string, json.RawMessage, bool, error) {
	var input protocol.TaskArtifactHandoff
	if err := decodeOperationCommand(op.PayloadJSON, &input); err != nil {
		return "", nil, false, err
	}
	root, err := scanner.ResolveWorkspaceRoot(input.Workspace, c.inventoryRoots())
	if err != nil {
		return "", nil, false, err
	}
	if input.Action == "apply" {
		if err := requireTaskReviewWorktree(root, input.Workspace); err != nil {
			return "", nil, false, err
		}
	}
	if err = gitrepository.HandoffTaskArtifact(ctx, root, input); err != nil {
		if errors.Is(err, gitrepository.ErrArtifactHandoffUnknown) {
			raw, _ := json.Marshal(protocol.TaskArtifactReceipt{Digest: input.Digest, Action: input.Action, Unknown: true})
			return "", raw, true, err
		}
		return "", nil, false, err
	}
	raw, err := json.Marshal(protocol.TaskArtifactReceipt{Digest: input.Digest, Action: input.Action})
	return "", raw, err == nil && input.Action == "apply", err
}

func (c *Client) verifyTaskReview(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	switch envelope.Kind {
	case protocol.OpCodexTurnStart, protocol.OpClaudeTurnStart, protocol.OpGeminiTurnStart:
	default:
		return nil
	}
	var input struct {
		Review *protocol.TaskArtifactHandoff `json:"task_review_snapshot"`
	}
	if json.Unmarshal(envelope.PayloadJSON, &input) != nil || input.Review == nil {
		return nil
	}
	root, err := scanner.ResolveWorkspaceRoot(input.Review.Workspace, c.inventoryRoots())
	if err != nil {
		return err
	}
	if err := requireTaskReviewWorktree(root, input.Review.Workspace); err != nil {
		return err
	}
	input.Review.Action = "verify"
	return gitrepository.HandoffTaskArtifact(ctx, root, *input.Review)
}

func requireTaskReviewWorktree(resolved, requested string) error {
	if filepath.Clean(requested) != resolved {
		return errors.New("review needs a canonical worktree path")
	}
	gitFile, err := os.Lstat(filepath.Join(resolved, ".git"))
	if err != nil || !gitFile.Mode().IsRegular() {
		return errors.New("review needs an independent linked Git worktree")
	}
	return nil
}
