import { expect, test } from "vitest";
import type { Workspace } from "../types";
import { sessionKey, type AISession } from "./types";
import { includeSelection, unifiedGroups, workspaceTitle } from "./unifiedGroups";
const workspace = (id: string, project = "Project", extra = {}): Workspace => ({ id, project_id: id, project_name: project, server_id: "s", server_name: "Server", path: `/srv/${id}`, display_name: "", branch: "", status: "ready", ...extra } as Workspace);
const row = (engine: AISession["engine"], id: string, w = "w", extra = {}): AISession => ({ engine, id, workspace_id: w, project_id: w, project_name: "Project", server_id: "s", server_name: "Server", path: `/srv/${w}`, title: id, native_id: `native-${id}`, archived_at: null, updated_at: "2026-11-15T00:00:00Z", ...extra } as AISession);

test("mixes engines with pins, timestamps and stable identity, then sorts complete workspace groups", () => {
  const rows = [row("codex", "same"), row("claude", "same", "w", { updated_at: "2026-11-16" }), row("gemini", "g", "w", { pinned_at: "2026-11-14" })];
  const workspaces = [workspace("w", "Beta"), workspace("a", "Alpha"), workspace("pin", "Zebra", { project_pinned_at: "2026-11-13" })];
  const summaries = workspaces.map(w => ({ workspace_id: w.id, matched_count: 3, latest_updated_at: "", project_pinned_at: w.project_pinned_at ?? null }));
  const groups = unifiedGroups(rows, workspaces, summaries, false);
  expect(groups.map(g => g.id)).toEqual(["pin", "a", "w"]);
  expect(groups[2].threads.map(sessionKey)).toEqual(["gemini:g", "claude:same", "codex:same"]);
  expect(groups[1].threads).toEqual([]);
  expect(groups[1].count).toBe(3);
});

test("orders equal project names by canonical workspace title and uses scoped project preferences", () => {
  const workspaces = [workspace("b", "Legacy", { project_display_name: "Scoped", display_name: "Beta" }), workspace("a", "Legacy", { project_display_name: "Scoped", display_name: "Alpha" })];
  expect(unifiedGroups([], workspaces, [], true).map(g => g.name)).toEqual(["Scoped · Alpha", "Scoped · Beta"]);
  expect(workspaceTitle(workspace("identity", "identity", { path: "/", display_name: "identity" }))).toBe("identity");
});

test("shows empty workspaces only on request, filters them by group search and excludes hidden projects", () => {
  const workspaces = [workspace("w"), workspace("empty"), workspace("hidden", "Hidden", { project_hidden_at: "now" })];
  const summaries = [{ workspace_id: "w", matched_count: 80, latest_updated_at: "", project_pinned_at: null }, { workspace_id: "hidden", matched_count: 10, latest_updated_at: "", project_pinned_at: null }];
  expect(unifiedGroups([], workspaces, summaries, false).map(g => [g.id, g.count])).toEqual([["w", 80]]);
  expect(unifiedGroups([], workspaces, summaries, true).map(g => g.id)).toEqual(["empty", "w"]);
  expect(unifiedGroups([], workspaces, [], true, "empty").map(g => g.id)).toEqual(["empty"]);
});

test("merges a deep-linked selection beyond the current window using engine and ID together", () => {
  const items = [row("codex", "same")], selected = row("gemini", "same", "outside");
  const merged = includeSelection(items, selected, false, "", "", []);
  expect(merged.map(sessionKey)).toEqual(["codex:same", "gemini:same"]);
  expect(unifiedGroups(merged, [workspace("w"), workspace("outside")], [], false).map(g => g.id)).toEqual(["outside", "w"]);
  expect(includeSelection(merged, selected, false, "", "", [])).toBe(merged);
});

test("selection follows engine, archive, hidden and group/title/native-ID search filters", () => {
  const selected = row("gemini", "deep");
  expect(includeSelection([], selected, false, "claude", "", [])).toEqual([]);
  expect(includeSelection([], selected, true, "", "", [])).toEqual([]);
  expect(includeSelection([], { ...selected, project_hidden_at: "now" }, false, "", "", [])).toEqual([]);
  expect(includeSelection([], selected, false, "", "native-deep", [])).toEqual([selected]);
  expect(includeSelection([], selected, false, "", "Feature", [workspace("w", "Project", { display_name: "Feature" })])).toEqual([selected]);
  expect(includeSelection([], selected, false, "", "missing", [])).toEqual([]);
});
