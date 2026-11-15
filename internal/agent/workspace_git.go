package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wio-platform/wio/internal/gitrepository"
	"github.com/wio-platform/wio/internal/protocol"
)

func (c *Client) inspectWorkspaceGit(ctx context.Context, command protocol.GitWorkspaceInspectCommand) (protocol.GitWorkspaceInspectResult, error) {
	if strings.TrimSpace(command.WorkspaceID) == "" {
		return protocol.GitWorkspaceInspectResult{}, errors.New("workspace ID is required")
	}
	status, err := gitrepository.GetStatus(ctx, command.Path, c.inventoryRoots())
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	branches, err := gitrepository.ListBranches(ctx, command.Path, c.inventoryRoots())
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	remotes, err := gitrepository.ListRemotes(ctx, command.Path, c.inventoryRoots())
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	limit := command.CommitLimit
	if limit <= 0 {
		limit = 50
	}
	commits, err := gitrepository.ListCommits(ctx, command.Path, c.inventoryRoots(), gitrepository.CommitLogOptions{Limit: limit})
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	changes, err := gitrepository.ListWorkspaceChanges(ctx, command.Path, c.inventoryRoots())
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	result := protocol.GitWorkspaceInspectResult{WorkspaceID: command.WorkspaceID, Status: protocol.GitStatus{Branch: status.Branch, Detached: status.Detached, Unborn: status.Unborn, Head: status.Head, Upstream: status.Upstream, Ahead: status.Ahead, Behind: status.Behind, Staged: status.Staged, Unstaged: status.Unstaged, Untracked: status.Untracked, Dirty: status.Dirty}, HasMore: commits.HasMore}
	result.Changes = make([]protocol.WorkspaceChange, 0, len(changes))
	for _, change := range changes {
		result.Changes = append(result.Changes, protocol.WorkspaceChange{Path: change.Path, OldPath: change.OldPath, Status: change.Status, Staged: change.Staged, Unstaged: change.Unstaged})
	}
	result.Branches = make([]protocol.GitBranch, 0, len(branches))
	for _, branch := range branches {
		result.Branches = append(result.Branches, protocol.GitBranch{Name: branch.Name, FullName: branch.FullName, Kind: branch.Kind, CommitSHA: branch.CommitSHA, Upstream: branch.Upstream, Current: branch.Current})
	}
	result.Remotes = make([]protocol.GitRemote, 0, len(remotes))
	for _, remote := range remotes {
		result.Remotes = append(result.Remotes, protocol.GitRemote{Name: remote.Name, FetchURLs: remote.FetchURLs, PushURLs: remote.PushURLs})
	}
	result.Commits = make([]protocol.GitCommit, 0, len(commits.Commits))
	for _, commit := range commits.Commits {
		result.Commits = append(result.Commits, protocol.GitCommit{SHA: commit.SHA, AuthorName: commit.AuthorName, AuthorEmail: commit.AuthorEmail, AuthoredAt: commit.AuthoredAt, Title: commit.Title, Parents: commit.Parents})
	}
	return result, nil
}

func (c *Client) writeWorkspaceGit(ctx context.Context, command protocol.GitWorkspaceWriteCommand) (protocol.GitWorkspaceWriteResult, error) {
	if command.WorkspaceID == "" || command.Path == "" || command.Action == "" {
		return protocol.GitWorkspaceWriteResult{}, errors.New("workspace Git write command is incomplete")
	}
	roots := c.inventoryRoots()
	if command.Action == "checkout" || command.Action == "pull" {
		status, statusErr := gitrepository.GetStatus(ctx, command.Path, roots)
		if statusErr != nil {
			return protocol.GitWorkspaceWriteResult{}, statusErr
		}
		if status.Dirty {
			return protocol.GitWorkspaceWriteResult{}, errors.New("workspace has uncommitted changes")
		}
	}
	var err error
	switch command.Action {
	case "branch.create":
		err = gitrepository.CreateBranch(ctx, command.Path, command.Branch, command.StartPoint, roots)
	case "branch.rename":
		err = gitrepository.RenameBranch(ctx, command.Path, command.Branch, command.NewBranch, roots)
	case "branch.delete":
		err = gitrepository.DeleteBranch(ctx, command.Path, command.Branch, command.Force, roots)
	case "checkout":
		err = gitrepository.Checkout(ctx, command.Path, command.Ref, command.Detach, roots)
	case "remote.add":
		err = gitrepository.AddRemote(ctx, command.Path, command.Remote, command.URL, roots)
	case "remote.set-url":
		err = gitrepository.SetRemoteURL(ctx, command.Path, command.Remote, command.URL, roots)
	case "remote.remove":
		err = gitrepository.RemoveRemote(ctx, command.Path, command.Remote, roots)
	case "fetch":
		err = gitrepository.Fetch(ctx, command.Path, command.Remote, roots)
	case "pull":
		err = gitrepository.Pull(ctx, command.Path, command.Remote, command.Branch, roots)
	case "push":
		err = gitrepository.Push(ctx, command.Path, command.Remote, command.Ref, command.SetUpstream, roots)
	case "stage":
		err = gitrepository.Stage(ctx, command.Path, command.Paths, command.All, roots)
	case "unstage":
		err = gitrepository.Unstage(ctx, command.Path, command.Paths, command.All, roots)
	case "discard":
		if command.IncludeStaged {
			err = gitrepository.DiscardChanges(ctx, command.Path, command.Paths, command.All, roots)
		} else {
			err = gitrepository.DiscardUnstaged(ctx, command.Path, command.Paths, command.All, roots)
		}
	case "commit":
		if command.All {
			err = gitrepository.Stage(ctx, command.Path, nil, true, roots)
		}
		if err != nil {
			break
		}
		err = gitrepository.CommitChanges(ctx, command.Path, command.Message, roots)
	default:
		return protocol.GitWorkspaceWriteResult{}, fmt.Errorf("unsupported workspace Git action %q", command.Action)
	}
	if err != nil {
		return protocol.GitWorkspaceWriteResult{}, err
	}
	snapshot, err := c.inspectWorkspaceGit(ctx, protocol.GitWorkspaceInspectCommand{WorkspaceID: command.WorkspaceID, Path: command.Path, CommitLimit: 50})
	if err != nil {
		return protocol.GitWorkspaceWriteResult{}, err
	}
	return protocol.GitWorkspaceWriteResult{WorkspaceID: command.WorkspaceID, Action: command.Action, Snapshot: snapshot}, nil
}
