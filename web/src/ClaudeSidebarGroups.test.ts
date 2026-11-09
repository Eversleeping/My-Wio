import { expect, test } from "vitest";
import type { ClaudeThread, Workspace } from "./types";
import { groupClaudeThreads } from "./pages/ClaudePage";

test("sidebar omits workspaces without sessions", () => {
  const thread = { id: "t", title: "Task", workspace_id: "main", path: "/repo", project_name: "My-Wio", project_id: "p", server_id: "s", server_name: "腾讯云", session_id: "", project_pinned_at: null, project_hidden_at: null } as unknown as ClaudeThread;
  const workspaces = [
    { id: "main", project_id: "p", server_id: "s", path: "/repo", kind: "primary", status: "ready" },
    { id: "empty", project_id: "p", server_id: "s", path: "/repo-gemini-wio-5f0b3329", kind: "worktree", branch: "gemini/wio-5f0b3329", status: "ready" }
  ] as unknown as Workspace[];
  const groups = groupClaudeThreads([thread], workspaces, "");
  expect(groups.map(([id]) => id)).toEqual(["main"]);
  expect(groupClaudeThreads([], workspaces, "")).toEqual([]);
});
