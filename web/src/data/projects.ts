import type { Project,ProjectDeletionPlan,ProjectDetail,Workspace,WorkspaceDeletionPlan,WorkspaceGitSnapshot } from "../types";
import type { CreateProjectRequest,WorkspaceGitAction } from "./projectTypes";
import { mutation,query,writeResource } from "./resources";

function resourcePath(path: string, values: Record<string, string>) {
  const params = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => { if (value) params.set(key, value); });
  const query = params.toString();
  return query ? `${path}?${query}` : path;
}

export const projects = {
  list: (filter: Record<string, string> = {}) => query<Project[]>(resourcePath("/projects", filter), ["projects"]),
  workspaces: (filter: Record<string, string> = {}) => query<Workspace[]>(resourcePath("/workspaces", filter), ["projects", "deployments"]),
  hidden: () => query<Pick<Workspace, "project_id" | "server_id">[]>("/projects/hidden", ["projects"]),
  detail: (id: string) => query<ProjectDetail>(`/projects/${id}`, ["projects"]),
  git: (id: string) => query<WorkspaceGitSnapshot>(`/workspaces/${id}/git`, ["projects"]),
  create: (input: Extract<CreateProjectRequest, {mode: "blank"}>) => writeResource(mutation("/projects", "POST", ["projects"]), input),
  import: (input: {remote_url: string; name: string; server_id: string; destination: string}) => writeResource(mutation("/projects/import", "POST", ["projects"]), input),
  discover: (serverID: string) => writeResource(mutation("/projects/discover", "POST", ["projects"]), {server_id: serverID}),
  retry: (id: string, blank: boolean) => writeResource(mutation(`/projects/${id}/${blank ? "retry-create" : "retry-import"}`, "POST", ["projects"]), {}),
  deletionPlan: (id: string) => writeResource(mutation<ProjectDeletionPlan>(`/projects/${id}/deletion-plan`, "POST", []), {}),
  remove: (id: string, mode: "metadata-only" | "managed-files") => writeResource(mutation(`/projects/${id}`, "DELETE", ["projects"]), {mode}),
  restore: (id: string, serverID: string) => writeResource(mutation<Project>(`/projects/${id}`, "PATCH", ["projects"]), {server_id: serverID, hidden: false}),
  update: (id: string, input: Pick<Project, "name" | "description" | "default_branch"> & {pinned: boolean; hidden: boolean; archived: boolean}) => writeResource(mutation(`/projects/${id}`, "PATCH", ["projects"]), input),
  refreshGit: (id: string) => writeResource(mutation(`/workspaces/${id}/git/refresh`, "POST", ["projects"]), {}),
  renameWorkspace: (id: string, name: string) => writeResource(mutation(`/workspaces/${id}`, "PATCH", ["projects"]), {display_name: name}),
  moveWorkspace: (id: string, path: string) => writeResource(mutation(`/workspaces/${id}/move`, "POST", ["projects"]), {path}),
  copyWorkspace: (id: string, serverID: string, path: string) => writeResource(mutation(`/workspaces/${id}/copy`, "POST", ["projects"]), {server_id: serverID, path}),
  workspaceDeletionPlan: (id: string, force: boolean) => writeResource(mutation<WorkspaceDeletionPlan>(`/workspaces/${id}/deletion-plan?force=${force}`, "POST", []), {}),
  removeWorkspace: (id: string, mode: "metadata" | "files", force: boolean) => writeResource(mutation(`/workspaces/${id}?mode=${mode}&force=${force}`, "DELETE", ["projects"])),
  gitAction: async (id: string, action: WorkspaceGitAction) => {
    const base = `/workspaces/${id}/git`;
    const segment = (value: string) => encodeURIComponent(value);
    const send = (method: "POST" | "PATCH" | "DELETE", path: string, body?: unknown) => writeResource(mutation(path, method, ["projects"]), body);
    switch (action.type) {
      case "branch.create": await send("POST", `${base}/branches`, { name: action.name, start_point: action.startPoint }); break;
      case "branch.rename": await send("PATCH", `${base}/branches/${segment(action.branch)}`, { name: action.name }); break;
      case "branch.delete": await send("DELETE", `${base}/branches/${segment(action.branch)}?force=${action.force}`); break;
      case "checkout": await send("POST", `${base}/checkout`, { ref: action.ref, detach: action.detach }); break;
      case "remote.add": await send("POST", `${base}/remotes`, { name: action.name, url: action.url }); break;
      case "remote.update": await send("PATCH", `${base}/remotes/${segment(action.remote)}`, { url: action.url }); break;
      case "remote.delete": await send("DELETE", `${base}/remotes/${segment(action.remote)}`); break;
      case "fetch": await send("POST", `${base}/fetch`, { remote: action.remote }); break;
      case "pull": await send("POST", `${base}/pull`, { remote: action.remote, branch: action.branch }); break;
      case "push": await send("POST", `${base}/push`, { remote: action.remote, ref: action.ref, set_upstream: action.setUpstream }); break;
      case "stage": await send("POST", `${base}/stage`, { paths: action.paths, all: action.all }); break;
      case "unstage": await send("POST", `${base}/unstage`, { paths: action.paths, all: action.all }); break;
      case "discard": await send("POST", `${base}/discard`, { paths: action.paths, all: action.all }); break;
      case "commit": await send("POST", `${base}/commit`, { message: action.message }); break;
    }
  },
};
