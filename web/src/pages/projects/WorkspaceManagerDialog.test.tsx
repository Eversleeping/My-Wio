import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import type { Server, Workspace, WorkspaceDeletionPlan } from "../../types";
import { WorkspaceManagerDialog, type WorkspaceManagerLabels } from "./WorkspaceManagerDialog";
import type { DialogSlotProps } from "./slots";

const labels: WorkspaceManagerLabels = {
  title: "Manage", rename: "Rename", move: "Move", copy: "Copy", delete: "Delete", displayName: "Name", currentPath: "Current path", targetPath: "Target path", targetServer: "Target server", sameServer: "Current server", managedOnly: "Managed only", save: "Save", moving: "Moving", copying: "Copying", loadingPlan: "Loading plan", metadataOnly: "Metadata only", deleteFiles: "Delete files", metadataDescription: "Keep files", filesDescription: "Remove files", dirty: "Dirty", activeOperations: "Operations", threads: "Sessions", childWorkspaces: "Children", force: "Force", blockers: "Checks", noBlockers: "No blockers", confirmLabel: "Confirm", confirmPlaceholder: "Type {name}", deleting: "Deleting", cancel: "Cancel"
};
const workspace: Workspace = { id: "workspace-1", project_id: "project-1", server_id: "server-1", path: "/srv/alpha", display_name: "alpha-main", management_mode: "managed", status: "ready", branch: "main", commit_sha: "abc", dirty: 0, last_git_refresh_at: null, git_error: "", kind: "primary", parent_workspace_id: null, server_name: "server", project_name: "alpha" };
const server: Server = { id: "server-1", name: "server", hostname: "server.local", status: "online", agent_version: "", agent_target_version: "", agent_update_available: false, agent_update_supported: false, codex_version: "", codex_ready: 1, codex_target_version: "", codex_update_available: false, codex_update_supported: false, address: "", configuration: "", notes: "", codex_profile_id: "", codex_profile_name: "", git_profile_id: "", git_profile_name: "", last_seen_at: null, created_at: "" };
const plan: WorkspaceDeletionPlan = { workspace_id: workspace.id, path: workspace.path, managed: true, dirty: false, active_operations: 0, thread_count: 0, child_workspaces: 0, can_remove_record: true, can_delete_files: true, record_blockers: [], file_blockers: [], blockers: [] };
function Dialog({ open, title, children, className }: DialogSlotProps) { return open ? <div role="dialog" aria-label={title} className={className}>{children}</div> : null; }

test("loads the deletion plan, refreshes force mode, and confirms file deletion", async () => {
  const user = userEvent.setup();
  const onLoad = vi.fn();
  const onDelete = vi.fn();
  render(<WorkspaceManagerDialog open workspace={workspace} servers={[server]} plan={plan} planLoading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRename={vi.fn()} onMove={vi.fn()} onCopy={vi.fn()} onLoadDeletionPlan={onLoad} onDelete={onDelete} />);

  await user.click(screen.getByRole("tab", { name: "Delete" }));
  expect(onLoad).toHaveBeenCalledWith(false);
  expect(screen.getByRole("dialog")).toHaveClass("workspace-manager-dialog-shell");
  const summary = screen.getByText("Dirty").closest(".deletion-summary-grid");
  expect(summary).toHaveClass("workspace-deletion-summary");
  expect(summary).not.toHaveClass("workspace");
  expect(screen.getByRole("tabpanel")).toHaveClass("workspace-manager-content");
  await user.click(screen.getByRole("tab", { name: "Delete files" }));
  await user.click(screen.getByRole("checkbox", { name: "Force" }));
  expect(onLoad).toHaveBeenLastCalledWith(true);
  await user.type(screen.getByPlaceholderText("Type alpha-main"), "alpha-main");
  await user.click(screen.getByRole("button", { name: "Delete" }));
  expect(onDelete).toHaveBeenCalledWith("files", true);
});

test("disables move and copy for observed workspaces", () => {
  render(<WorkspaceManagerDialog open workspace={{ ...workspace, management_mode: "observed" }} servers={[server]} plan={null} planLoading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRename={vi.fn()} onMove={vi.fn()} onCopy={vi.fn()} onLoadDeletionPlan={vi.fn()} onDelete={vi.fn()} />);
  expect(screen.getByRole("tab", { name: "Move" })).toBeDisabled();
  expect(screen.getByRole("tab", { name: "Copy" })).toBeDisabled();
});

test("shows blockers for the selected deletion mode only", async () => {
  const user = userEvent.setup();
  const dirtyPlan: WorkspaceDeletionPlan = { ...plan, dirty: true, can_delete_files: false, record_blockers: [], file_blockers: ["Uncommitted changes"], blockers: ["Uncommitted changes"] };
  render(<WorkspaceManagerDialog open workspace={workspace} servers={[server]} plan={dirtyPlan} planLoading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRename={vi.fn()} onMove={vi.fn()} onCopy={vi.fn()} onLoadDeletionPlan={vi.fn()} onDelete={vi.fn()} />);

  await user.click(screen.getByRole("tab", { name: "Delete" }));
  expect(screen.getByText("No blockers")).toBeInTheDocument();
  expect(screen.queryByText("Uncommitted changes")).not.toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: "Delete files" }));
  expect(screen.getByText("Uncommitted changes")).toBeInTheDocument();
});

test("submits managed workspace move and copy destinations", async () => {
  const user = userEvent.setup();
  const onMove = vi.fn();
  const onCopy = vi.fn();
  render(<WorkspaceManagerDialog open workspace={workspace} servers={[server]} plan={null} planLoading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onRename={vi.fn()} onMove={onMove} onCopy={onCopy} onLoadDeletionPlan={vi.fn()} onDelete={vi.fn()} />);
  await user.click(screen.getByRole("tab", { name: "Move" }));
  await user.type(screen.getByRole("textbox", { name: "Target path" }), "/srv/alpha-moved");
  await user.click(screen.getAllByRole("button", { name: "Move" }).at(-1)!);
  expect(onMove).toHaveBeenCalledWith("/srv/alpha-moved");

  await user.click(screen.getByRole("tab", { name: "Copy" }));
  const targetPath = screen.getByRole("textbox", { name: "Target path" });
  await user.clear(targetPath);
  await user.type(targetPath, "/srv/alpha-copy");
  await user.click(screen.getAllByRole("button", { name: "Copy" }).at(-1)!);
  expect(onCopy).toHaveBeenCalledWith("server-1", "/srv/alpha-copy");
});
