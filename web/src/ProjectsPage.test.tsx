import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "./i18n";
import ProjectsPage from "./pages/ProjectsPage";
import type { Project, Workspace } from "./types";

const now = "2026-10-28T00:00:00Z";
const project: Project = {
  id: "shared-project", name: "same", description: "", remote_url: "https://example.com/same.git", default_branch: "main", status: "ready", provision_error: "", updated_at: now, workspace_count: 4, import_status: "", import_message: "", import_server_id: "", import_server_name: "", import_operation_id: "", pinned_at: null, hidden_at: now, archived_at: null
};
function workspace(id: string, serverID: string, projectID = project.id): Workspace {
  return { id, project_id: projectID, server_id: serverID, server_name: serverID, project_name: "same", path: `/srv/${id}`, display_name: id, management_mode: "managed", status: "ready", branch: "main", commit_sha: "abc1234", dirty: 0, last_git_refresh_at: null, git_error: "", kind: "repository", parent_workspace_id: null };
}
const workspaces = [workspace("a", "server-a"), workspace("a-child", "server-a"), workspace("b", "server-b"), workspace("c", "server-c"), workspace("other-project", "server-a", "other-project")];

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); window.history.replaceState(null, "", "/"); });

function setup(failRestore = false) {
  window.localStorage.setItem("wio_language", "en");
  const hiddenServers = new Set(["server-a", "server-b"]);
  const requests: { url: string; body: Record<string, unknown> }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    let body: unknown = [];
    if (init.method === "PATCH") {
      const patch = JSON.parse(String(init.body));
      requests.push({ url, body: patch });
      if (failRestore) return new Response(JSON.stringify({ error: "restore failed" }), { status: 500 });
      hiddenServers.delete(patch.server_id);
      body = project;
    } else if (url === "/api/projects") body = [project];
    else if (url === "/api/workspaces") body = workspaces;
    else if (url === "/api/projects/hidden") body = [...hiddenServers].map(serverID => ({ project_id: project.id, server_id: serverID, project_name: project.name, server_name: serverID, hidden_at: now }));
    return new Response(JSON.stringify(body));
  }));
  const notify = vi.fn();
  render(<I18nProvider><ProjectsPage realtime={0} notify={notify} /></I18nProvider>);
  return { requests, notify };
}

function row(id: string) {
  return within(screen.getByTitle(`/srv/${id}`).closest("tr")!);
}

test("restores the selected workspace's server project without restoring another server", async () => {
  const { requests, notify } = setup();
  const user = userEvent.setup();
  await screen.findAllByRole("button", { name: "Restore" });
  const projectTable = screen.getAllByRole("table")[0];
  expect(within(projectTable).queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
  expect(row("a").getByRole("button", { name: "Restore" })).toBeEnabled();
  expect(row("b").getByRole("button", { name: "Restore" })).toBeEnabled();
  expect(row("c").queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
  expect(row("other-project").queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();

  await user.click(row("a-child").getByRole("button", { name: "Restore" }));
  await waitFor(() => expect(row("a-child").queryByRole("button", { name: "Restore" })).not.toBeInTheDocument());
  expect(row("a").queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
  expect(row("b").getByRole("button", { name: "Restore" })).toBeEnabled();
  expect(requests).toEqual([{ url: "/api/projects/shared-project", body: { server_id: "server-a", hidden: false } }]);
  expect(notify).toHaveBeenCalledWith("Project restored");
});

test("keeps the workspace restore action available when restoration fails", async () => {
  const { requests, notify } = setup(true);
  const user = userEvent.setup();
  await screen.findAllByRole("button", { name: "Restore" });
  await user.click(row("b").getByRole("button", { name: "Restore" }));
  await waitFor(() => expect(notify).toHaveBeenCalledWith("restore failed"));
  expect(row("b").getByRole("button", { name: "Restore" })).toBeEnabled();
  expect(row("a").getByRole("button", { name: "Restore" })).toBeEnabled();
  expect(requests).toEqual([{ url: "/api/projects/shared-project", body: { server_id: "server-b", hidden: false } }]);
  expect(notify).not.toHaveBeenCalledWith("Project restored");
});
