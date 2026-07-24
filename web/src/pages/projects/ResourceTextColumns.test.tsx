import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import type { Project, Workspace } from "../../types";
import { ProjectTable, type ProjectTableLabels } from "./ProjectTable";
import type { DataTableSlotProps, StatusSlotProps } from "./slots";
import { WorkspaceTable, type WorkspaceTableLabels } from "./WorkspaceTable";

const remoteURL = "https://github.com/Eversleeping/my-choice-repository.git";
const workspacePath = "/var/lib/wio-agent/projects/gaokaoweb-feature-worktree";

const project: Project = {
  id: "project-1", name: "gaokaoweb", description: "", remote_url: remoteURL, default_branch: "main", status: "ready", provision_error: "", updated_at: "2026-01-01T00:00:00Z", workspace_count: 1, import_status: "", import_message: "", import_server_id: "", import_server_name: "", import_operation_id: "", pinned_at: null, hidden_at: null, archived_at: null
};

const workspace: Workspace = {
  id: "workspace-1", project_id: project.id, server_id: "server-1", path: workspacePath, display_name: "gaokaoweb feature", management_mode: "managed", status: "ready", branch: "feature/ui", commit_sha: "abcdef123456", dirty: 0, last_git_refresh_at: null, git_error: "", kind: "worktree", parent_workspace_id: null, server_name: "server-1", project_name: project.name
};

const projectLabels: ProjectTableLabels = {
  project: "Project", remote: "Remote", workspaces: "Workspaces", status: "Status", updated: "Updated", actions: "Actions", empty: "Empty", local: "Local", hidden: "Hidden", targetServer: server => server, awaitingWorkspace: "Waiting"
};

const workspaceLabels: WorkspaceTableLabels = {
  project: "Project", server: "Server", path: "Path", branch: "Branch", commit: "Commit", state: "State", actions: "Actions", empty: "Empty", detached: "Detached"
};

function DataTable({ headers, children }: DataTableSlotProps) {
  return <table><thead><tr>{headers.map(header => <th key={header}>{header}</th>)}</tr></thead><tbody>{children}</tbody></table>;
}

function Status({ value }: StatusSlotProps) { return <span>{value}</span>; }

test("keeps repository and workspace values available as overflow tooltips", () => {
  render(<>
    <ProjectTable projects={[project]} labels={projectLabels} slots={{ DataTable, Status }} formatTime={value => value} />
    <WorkspaceTable workspaces={[workspace]} labels={workspaceLabels} slots={{ DataTable, Status }} formatCommit={value => value} />
  </>);

  const repository = screen.getByTitle(remoteURL);
  const path = screen.getByTitle(workspacePath);
  expect(repository).toHaveClass("truncate-code");
  expect(repository.closest("td")).toHaveClass("fluid-text-cell");
  expect(path).toHaveClass("truncate-code");
  expect(path.closest("td")).toHaveClass("fluid-text-cell");
});

test("keeps an existing project ready when another server import fails", () => {
  const failedSecondaryImport: Project = {
    ...project,
    import_status: "failed",
    import_message: "clone failed",
    import_server_id: "server-2",
    import_server_name: "server-2"
  };

  render(<ProjectTable projects={[failedSecondaryImport]} labels={projectLabels} slots={{ DataTable, Status }} formatTime={value => value} />);

  expect(screen.getByText("ready")).toBeInTheDocument();
  expect(screen.queryByText("failed")).not.toBeInTheDocument();
  expect(screen.queryByText("clone failed")).not.toBeInTheDocument();
  expect(screen.queryByText("server-2")).not.toBeInTheDocument();
});
