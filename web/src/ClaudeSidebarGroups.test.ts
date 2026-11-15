import { expect, test } from "vitest";
import type { ClaudeThread, Workspace } from "./types";
import { groupSessions, withSelectedSession } from "./sessions/groups";

test("sidebar omits workspaces without sessions", () => {
  const thread = { id: "t", title: "Task", workspace_id: "main", path: "/repo", project_name: "My-Wio", project_id: "p", server_id: "s", server_name: "腾讯云", session_id: "", project_pinned_at: null, project_hidden_at: null } as unknown as ClaudeThread;
  const workspaces = [
    { id: "main", project_id: "p", server_id: "s", path: "/repo", kind: "primary", status: "ready" },
    { id: "empty", project_id: "p", server_id: "s", path: "/repo-gemini-wio-5f0b3329", kind: "worktree", branch: "gemini/wio-5f0b3329", status: "ready" }
  ] as unknown as Workspace[];
  const groups = groupSessions([thread], workspaces);
  expect(groups.map(({id}) => id)).toEqual(["main"]);
  expect(groupSessions([], workspaces)).toEqual([]);
});

test("keeps full matching counts and workspace metadata outside the current page", () => {
  const workspace = {id: "tail", project_name: "Project", server_name: "Server", path: "/repo", branch: "feature"} as Workspace;
  const groups = groupSessions([], [workspace], [{workspace_id: "tail", matched_count: 72, latest_updated_at: "now", project_pinned_at: null}]);
  expect(groups).toEqual([expect.objectContaining({id: "tail", name: "Project · feature", count: 72, threads: []})]);
});

test("merges a deep link once and respects archive/search filters", () => {
  const thread = {id: "deep", engine: "claude", workspace_id: "w", archived_at: null, title: "Outside page", server_name: "Server", project_name: "Project", path: "/repo"} as unknown as ClaudeThread;
  expect(withSelectedSession([], thread, false, "")).toEqual([thread]);
  expect(withSelectedSession([thread], thread, false, "")).toHaveLength(1);
  expect(withSelectedSession([], thread, true, "")).toEqual([]);
  expect(withSelectedSession([], thread, false, "absent")).toEqual([]);
  expect(groupSessions(withSelectedSession([], thread, false, ""), [])).toEqual([expect.objectContaining({id:"w", threads:[thread]})]);
});
