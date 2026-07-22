import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import type { WorkspaceGitSnapshot } from "../../types";
import { WorkspaceGitDialog, type GitDialogLabels } from "./WorkspaceGitDialog";
import type { DialogSlotProps } from "./slots";

const labels: GitDialogLabels = {
  title: "Git", status: "Status", branches: "Branches", remotes: "Remotes", commits: "Commits", refresh: "Refresh", refreshing: "Refreshing", branch: "Branch", head: "Head", upstream: "Upstream", ahead: "Ahead", behind: "Behind", staged: "Staged", unstaged: "Unstaged", untracked: "Untracked", clean: "Clean", dirty: "Dirty", noBranches: "No branches", noRemotes: "No remotes", noCommits: "No commits", close: "Close", sync: "Sync", remote: "Remote", ref: "Ref", fetch: "Fetch", pull: "Pull", push: "Push", setUpstream: "Set upstream", createBranch: "Create branch", branchName: "Branch name", startPoint: "Start point", checkout: "Checkout", detach: "Detach", rename: "Rename", edit: "Edit", delete: "Delete", forceDelete: "Force delete", addRemote: "Add remote", remoteName: "Remote name", remoteURL: "Remote URL", save: "Save", cancel: "Cancel", current: "Current", local: "Local", remoteBranch: "Remote branch", actionQueued: "Queued"
};
const snapshot: WorkspaceGitSnapshot = {
  workspace_id: "workspace-1", status: "succeeded", error: "", requested_at: null, updated_at: null,
  data: { workspace_id: "workspace-1", status: { branch: "main", detached: false, unborn: false, head: "abcdef123456", upstream: "origin/main", ahead: 0, behind: 0, staged: 0, unstaged: 1, untracked: 0, dirty: true }, branches: [{ name: "main", full_name: "refs/heads/main", kind: "local", commit_sha: "abcdef123456", current: true }], remotes: [{ name: "origin", fetch_urls: ["https://example.com/repo.git"], push_urls: ["https://example.com/repo.git"] }], commits: [], has_more: false }
};
function Dialog({ open, title, children, className }: DialogSlotProps) { return open ? <div role="dialog" aria-label={title} className={className}>{children}</div> : null; }

test("creates a branch with a structured action", async () => {
  const user = userEvent.setup();
  const onAction = vi.fn().mockResolvedValue(undefined);
  render(<WorkspaceGitDialog open snapshot={snapshot} loading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRefresh={vi.fn()} onAction={onAction} />);
  await user.click(screen.getByRole("tab", { name: "Branches" }));
  await user.type(screen.getByRole("textbox", { name: "Branch name" }), "feature/ui");
  const start = screen.getByRole("textbox", { name: "Start point" });
  await user.clear(start);
  await user.type(start, "origin/main");
  await user.click(screen.getByRole("button", { name: "Create branch" }));
  await waitFor(() => expect(onAction).toHaveBeenCalledWith({ type: "branch.create", name: "feature/ui", startPoint: "origin/main" }));
});

test("allows fetch but blocks pull while the workspace is dirty", async () => {
  const user = userEvent.setup();
  const onAction = vi.fn().mockResolvedValue(undefined);
  render(<WorkspaceGitDialog open snapshot={snapshot} loading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRefresh={vi.fn()} onAction={onAction} />);
  expect(screen.getByRole("button", { name: "Pull" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Fetch" }));
  await waitFor(() => expect(onAction).toHaveBeenCalledWith({ type: "fetch", remote: "origin" }));
});

test("adds a remote and queues push with upstream", async () => {
  const user = userEvent.setup();
  const onAction = vi.fn().mockResolvedValue(undefined);
  render(<WorkspaceGitDialog open snapshot={snapshot} loading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRefresh={vi.fn()} onAction={onAction} />);
  await user.click(screen.getByRole("tab", { name: "Remotes" }));
  expect(screen.getByTitle("https://example.com/repo.git")).toHaveClass("truncate-code");
  await user.type(screen.getByRole("textbox", { name: "Remote name" }), "backup");
  await user.type(screen.getByRole("textbox", { name: "Remote URL" }), "https://example.com/backup.git");
  await user.click(screen.getByRole("button", { name: "Add remote" }));
  await waitFor(() => expect(onAction).toHaveBeenCalledWith({ type: "remote.add", name: "backup", url: "https://example.com/backup.git" }));

  await user.click(screen.getByRole("tab", { name: "Status" }));
  await user.click(screen.getByRole("checkbox", { name: "Set upstream" }));
  await user.click(screen.getByRole("button", { name: "Push" }));
  await waitFor(() => expect(onAction).toHaveBeenCalledWith({ type: "push", remote: "origin", ref: "main", setUpstream: true }));
});

test("renders legacy null collections as empty states", async () => {
  const user = userEvent.setup();
  const legacySnapshot: WorkspaceGitSnapshot = {
    ...snapshot,
    data: { ...snapshot.data, branches: null, remotes: null, commits: null }
  };
  render(<WorkspaceGitDialog open snapshot={legacySnapshot} loading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRefresh={vi.fn()} onAction={vi.fn()} />);

  expect(screen.getByRole("button", { name: "Fetch" })).toBeDisabled();
  await user.click(screen.getByRole("tab", { name: "Branches" }));
  expect(screen.getByText("No branches")).toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: "Remotes" }));
  expect(screen.getByText("No remotes")).toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: "Commits" }));
  expect(screen.getByText("No commits")).toBeInTheDocument();
  expect(screen.getByRole("dialog")).toHaveClass("workspace-git-dialog-shell");
  expect(screen.getByRole("tabpanel")).toHaveClass("workspace-git-tab-panel");
  expect(screen.getByText("No commits").closest(".workspace-git-tab-panel")).toBeInTheDocument();
});

test("shows the refresh state while Git data is being collected", () => {
  const refreshingSnapshot: WorkspaceGitSnapshot = { ...snapshot, status: "refreshing" };
  render(<WorkspaceGitDialog open snapshot={refreshingSnapshot} loading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRefresh={vi.fn()} onAction={vi.fn()} />);

  expect(screen.getByRole("status")).toHaveTextContent("Refreshing");
  expect(screen.getByRole("button", { name: "Refreshing" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Fetch" })).toBeDisabled();
});
