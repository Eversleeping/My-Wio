import { expect, test } from "vitest";
import { groupThreadsByWorkspace } from "./App";
import type { Thread, Workspace } from "./types";

const thread = (id: string, workspaceID: string, path: string): Thread => ({
  id,
  workspace_id: workspaceID,
  project_id: "project-1",
  codex_thread_id: `codex-${id}`,
  title: id,
  status: "idle",
  path,
  server_id: "server-1",
  server_name: "server-1",
  project_name: "gaokaoweb",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  pinned_at: null,
  archived_at: null,
  project_pinned_at: null,
  project_hidden_at: null
});

const workspace = (id: string, path: string, branch: string): Workspace => ({
  id,
  project_id: "project-1",
  server_id: "server-1",
  path,
  display_name: "gaokaoweb",
  management_mode: "managed",
  status: "ready",
  branch,
  commit_sha: "abc123",
  dirty: 0,
  last_git_refresh_at: null,
  git_error: "",
  kind: "primary",
  parent_workspace_id: null,
  server_name: "server-1",
  project_name: "gaokaoweb"
});

test("groups Codex sessions by workspace within the same project", () => {
  const groups = groupThreadsByWorkspace(
    [
      thread("main-1", "workspace-main", "/srv/gaokaoweb"),
      thread("feature-1", "workspace-feature", "/srv/gaokaoweb-test"),
      thread("feature-2", "workspace-feature", "/srv/gaokaoweb-test")
    ],
    [
      workspace("workspace-main", "/srv/gaokaoweb", "main"),
      workspace("workspace-feature", "/srv/gaokaoweb-test", "feature/free2")
    ]
  );

  expect(groups).toHaveLength(2);
  expect(groups.map(group => group.label)).toEqual([
    "gaokaoweb · feature/free2",
    "gaokaoweb · main"
  ]);
  expect(groups.find(group => group.workspaceID === "workspace-feature")?.threads).toHaveLength(2);
});
