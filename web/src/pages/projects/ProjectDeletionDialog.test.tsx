import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import type { Project, ProjectDeletionPlan } from "../../types";
import { ProjectDeletionDialog, type ProjectDeletionLabels } from "./ProjectDeletionDialog";
import type { DialogSlotProps } from "./slots";

const labels: ProjectDeletionLabels = {
  title: "Delete project", loading: "Loading", metadataOnly: "Metadata only", metadataDescription: "Keep files", managedFiles: "Managed files", managedDescription: "Delete files", workspaces: "Workspaces", managed: "Managed", observed: "Observed", dirty: "Dirty", activeOperations: "Operations", activeTasks: "Tasks", activeDeployments: "Deployments", remotePreserved: "Remote preserved", blockers: "Checks", noBlockers: "No blockers", confirmLabel: "Confirm", confirmPlaceholder: "Type {name}", cancel: "Cancel", deleting: "Deleting", deleteMetadata: "Delete metadata", deleteFiles: "Delete files"
};
const project: Project = { id: "project-1", name: "alpha", description: "", remote_url: "https://example.com/alpha.git", default_branch: "main", status: "ready", provision_error: "", updated_at: "2026-01-01T00:00:00Z", workspace_count: 1, import_status: "", import_message: "", import_server_id: "", import_server_name: "", import_operation_id: "", pinned_at: null, hidden_at: null, archived_at: null };
const plan: ProjectDeletionPlan = { project_id: project.id, project_name: project.name, remote_url: project.remote_url, remote_preserved: true, can_delete_metadata: true, can_delete_managed_files: false, workspace_count: 1, managed_workspace_count: 1, observed_workspace_count: 0, active_agent_operations: 0, active_codex_tasks: 0, active_deployments: 0, dirty_managed_workspaces: 1, offline_managed_servers: 0, workspaces: [], blockers: [{ code: "dirty-managed-workspaces", message: "Dirty workspace", count: 1, modes: ["managed-files"] }] };

function Dialog({ open, title, children }: DialogSlotProps) { return open ? <div role="dialog" aria-label={title}>{children}</div> : null; }

test("requires the project name and submits only an allowed deletion mode", async () => {
  const user = userEvent.setup();
  const onSubmit = vi.fn();
  render(<ProjectDeletionDialog open project={project} plan={plan} loading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onSubmit={onSubmit} />);

  await user.click(screen.getByRole("tab", { name: "Managed files" }));
  expect(screen.getByRole("button", { name: "Delete files" })).toBeDisabled();
  expect(screen.getByText(/Dirty workspace/)).toBeInTheDocument();

  await user.click(screen.getByRole("tab", { name: "Metadata only" }));
  const deleteButton = screen.getByRole("button", { name: "Delete metadata" });
  expect(deleteButton).toBeDisabled();
  await user.type(screen.getByPlaceholderText("Type alpha"), "alpha");
  await user.click(deleteButton);
  expect(onSubmit).toHaveBeenCalledWith("metadata-only");
});

test("submits managed file deletion when preflight allows it", async () => {
  const user = userEvent.setup();
  const onSubmit = vi.fn();
  const readyPlan = { ...plan, can_delete_managed_files: true, dirty_managed_workspaces: 0, blockers: [] };
  render(<ProjectDeletionDialog open project={project} plan={readyPlan} loading={false} busy={false} error="" labels={labels} Dialog={Dialog} onClose={vi.fn()} onSubmit={onSubmit} />);
  await user.click(screen.getByRole("tab", { name: "Managed files" }));
  await user.type(screen.getByPlaceholderText("Type alpha"), "alpha");
  await user.click(screen.getByRole("button", { name: "Delete files" }));
  expect(onSubmit).toHaveBeenCalledWith("managed-files");
});
