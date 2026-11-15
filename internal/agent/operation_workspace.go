package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/gitrepository"
	"github.com/wio-platform/wio/internal/gitworktree"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
	"path/filepath"
	"strings"
	"time"
)

func (c *Client) runWorkspaceFilesOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.WorkspaceFilesCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.WorkspaceFilesResult
		result, err = scanner.ListWorkspaceFiles(ctx, command.Path, c.inventoryRoots())
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runWorkspaceFileWriteOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.WorkspaceFileWriteCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		err = scanner.WriteWorkspaceFile(ctx, command, c.inventoryRoots())
	}

	return
}

func (c *Client) runWorkspaceFilePreviewOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.WorkspaceFilePreviewCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.WorkspaceFilePreviewResult
		result, err = scanner.ReadWorkspaceFile(ctx, command.Root, command.Path, c.inventoryRoots(), 1024*1024)
		if err == nil && envelope.Kind == protocol.OpWorkspaceImagePreview && result.Encoding != "base64" {
			err = errors.New("image preview only supports JPEG, PNG, GIF and WebP files")
		}
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runWorkspaceChangesOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.WorkspaceChangesCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var changes []gitrepository.WorkspaceChange
		changes, err = gitrepository.ListWorkspaceChanges(ctx, command.Path, c.inventoryRoots())
		if err == nil {
			result := protocol.WorkspaceChangesResult{Changes: make([]protocol.WorkspaceChange, 0, len(changes))}
			for _, change := range changes {
				result.Changes = append(result.Changes, protocol.WorkspaceChange{Path: change.Path, OldPath: change.OldPath, Status: change.Status, Staged: change.Staged, Unstaged: change.Unstaged})
			}
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runWorkspaceDiffPreviewOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.WorkspaceDiffCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var diff gitrepository.WorkspaceDiff
		diff, err = gitrepository.ReadWorkspaceDiff(ctx, command.Root, command.Path, command.OldPath, c.inventoryRoots(), gitrepository.MaxWorkspaceDiffBytes)
		if err == nil {
			result := protocol.WorkspaceDiffResult{Path: diff.Path, Content: diff.Content, Additions: diff.Additions, Deletions: diff.Deletions, Binary: diff.Binary, Truncated: diff.Truncated}
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runGitProjectCreateOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GitProjectCreateCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.GitProjectCreateResult
		result, err = c.createProject(ctx, command)
		if err == nil {
			resultData, err = json.Marshal(result)
			refreshInventory = err == nil
		}
	}

	return
}

func (c *Client) runGitProjectDeleteOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GitProjectDeleteCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.GitProjectDeleteResult
		result, err = c.deleteProject(ctx, command)
		if err == nil {
			resultData, err = json.Marshal(result)
			refreshInventory = err == nil
		}
	}

	return
}

func (c *Client) runGitWorkspaceInspectOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GitWorkspaceInspectCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.GitWorkspaceInspectResult
		result, err = c.inspectWorkspaceGit(ctx, command)
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runGitWorkspaceWriteOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GitWorkspaceWriteCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.GitWorkspaceWriteResult
		result, err = c.writeWorkspaceGit(ctx, command)
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runGitWorkspaceLifecycleOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GitWorkspaceLifecycleCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.GitWorkspaceLifecycleResult
		result.WorkspaceID, result.TargetWorkspaceID, result.Action, result.SourcePath = command.WorkspaceID, command.TargetWorkspaceID, command.Action, command.SourcePath
		switch command.Action {
		case "move":
			_, err = gitrepository.MoveWorkspace(ctx, command.SourcePath, command.TargetPath, c.managedRoots())
		case "copy":
			_, err = gitrepository.CopyWorkspace(ctx, command.SourcePath, command.TargetPath, c.managedRoots())
		case "delete":
			err = gitrepository.DeleteWorkspace(ctx, command.SourcePath, command.Force, c.managedRoots())
		default:
			err = fmt.Errorf("unsupported workspace lifecycle action %q", command.Action)
		}
		if err == nil {
			if command.Action != "delete" {
				result.TargetPath = filepath.Clean(command.TargetPath)
			}
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runGitWorkspaceCloneOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GitWorkspaceCloneCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		destination, destinationErr := projectCreateDestination(c.config.CloneRoot, command.Name, command.Destination)
		if destinationErr != nil {
			err = destinationErr
		} else {
			var cloned gitrepository.CloneWorkspaceResult
			cloned, err = gitrepository.CloneWorkspace(ctx, gitrepository.CloneWorkspaceOptions{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: destination, RemoteURL: command.RemoteURL, Branch: command.Branch, ExpectedHead: command.ExpectedHead}, c.managedRoots())
			if err == nil {
				resultData, err = json.Marshal(protocol.GitWorkspaceCloneResult{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: filepath.Clean(command.Destination), Branch: cloned.Branch, CommitSHA: cloned.CommitSHA})
			}
		}
	}

	return
}

func (c *Client) runGitWorktreeCreateOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GitWorktreeCreateCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.GitWorktreeCreateResult
		result, err = gitworktree.Create(ctx, command, c.inventoryRoots())
		if err == nil && command.ClaudeFork != nil {
			if command.ClaudeFork.SourceWorkspace != command.SourcePath || command.ClaudeFork.Workspace != result.Path {
				err = errors.New("invalid Claude worktree paths")
			} else {
				var fork protocol.ClaudeForkResult
				fork, err = claudeadapter.Snapshot(ctx, c.config.StateDir, *command.ClaudeFork)
				result.ClaudeSnapshotID = fork.SnapshotID
			}
			if err != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				cleanupErr := gitworktree.Remove(cleanupCtx, command.SourcePath, result.Path, command.Branch, c.inventoryRoots())
				cancel()
				if cleanupErr != nil {
					err = fmt.Errorf("Claude fork failed: %v; cleanup: %w", err, cleanupErr)
				}
			}
		}
		if err == nil && command.GeminiFork != nil {
			sourceRoot, rootErr := scanner.ResolveWorkspaceRoot(command.SourcePath, c.inventoryRoots())
			if rootErr != nil || sourceRoot != command.GeminiFork.SourceWorkspace || command.GeminiFork.Workspace != result.Path {
				err = errors.New("invalid Gemini worktree paths")
			} else {
				var fork protocol.GeminiForkResult
				fork, err = c.gemini.Fork(ctx, envelope.OperationID, *command.GeminiFork)
				result.GeminiSessionID = fork.SessionID
			}
			if err != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				cleanupErr := gitworktree.Remove(cleanupCtx, command.SourcePath, result.Path, command.Branch, c.inventoryRoots())
				cancel()
				if cleanupErr != nil {
					err = fmt.Errorf("Gemini fork failed: %v; worktree cleanup: %w", err, cleanupErr)
				}
			}
		}
		if err == nil && command.TargetThreadID != "" {
			forkCommand := protocol.ForkThreadCommand{SourceThreadID: command.SourceThreadID, TargetThreadID: command.TargetThreadID, CodexThread: command.CodexThread, WorkspaceID: command.TargetWorkspaceID, Workspace: result.Path, Title: command.Title, LastTurnID: command.LastTurnID, CutoffSequence: command.CutoffSequence}
			var fork protocol.ForkThreadResult
			fork, err = c.codex.ForkThread(ctx, forkCommand)
			if err != nil {
				cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
				cleanupErr := gitworktree.Remove(cleanupContext, command.SourcePath, result.Path, command.Branch, c.inventoryRoots())
				cancelCleanup()
				if cleanupErr != nil {
					err = fmt.Errorf("fork Codex thread: %v; cleanup worktree: %w", err, cleanupErr)
				}
			} else {
				result.CodexThread = fork.CodexThread
				result.LastTurnID = fork.LastTurnID
			}
		}
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runGitWorktreeCleanupOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GitWorktreeCleanupCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		err = gitworktree.Remove(ctx, command.SourcePath, command.TargetPath, command.Branch, c.inventoryRoots())
	}

	return
}

func (c *Client) runDeployContainerOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.ContainerActionCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		if command.Action == "" {
			command.Action = strings.TrimPrefix(envelope.Kind, protocol.OpDeployContainerPrefix)
		}
		var actionResult protocol.ContainerActionResult
		actionResult, err = c.runContainerAction(ctx, command)
		if marshaled, marshalErr := json.Marshal(actionResult); marshalErr != nil {
			err = marshalErr
		} else {
			resultData = marshaled
		}
	}

	return
}

func (c *Client) executeGitImport(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.GitImportCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	if _, err := scanner.Import(ctx, c.config.CloneRoot, command); err != nil {
		return err
	}
	return c.enqueueInventory(ctx)
}

func (c *Client) executeDeployStart(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.DeployCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.runDeployment(ctx, command)
}

func (c *Client) executeDeployRollback(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.RollbackCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.runRollback(ctx, command)
}
