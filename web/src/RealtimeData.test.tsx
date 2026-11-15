import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { SessionCollaboration } from "./components/SessionCollaboration";
import { SessionUsage } from "./components/SessionUsage";
import { DataProvider } from "./data/resources";
import { I18nProvider } from "./i18n";
import OperationsPage from "./pages/OperationsPage";
import { WorkspaceFilesPanel } from "./workspace/WorkspaceFilesPanel";

const response = (data: unknown) => new Response(JSON.stringify(data));
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); localStorage.clear(); });

test("operation results update the active operations page through the data revision", async () => {
  localStorage.setItem("wio_language", "en");
  let result = "Working";
  vi.stubGlobal("fetch", vi.fn(async () => response([{id: "operation", status: result === "Working" ? "running" : "succeeded", result, kind: "inventory.scan", server_name: "Node", created_at: new Date().toISOString(), updated_at: new Date().toISOString()}])));
  const view = (revision: number) => <I18nProvider><DataProvider revisions={{operations: revision}}><OperationsPage /></DataProvider></I18nProvider>;
  const rendered = render(view(0));
  expect(await screen.findByText("Working")).toBeInTheDocument();
  result = "Inventory complete"; rendered.rerender(view(1));
  expect(await screen.findByText("Inventory complete")).toBeInTheDocument();
});

test.each(["codex", "claude", "gemini"] as const)("%s usage updates through the native event revision", async provider => {
  localStorage.setItem("wio_language", "en");
  vi.useFakeTimers({toFake: ["Date"]});
  let tokens = 10;
  vi.stubGlobal("fetch", vi.fn(async () => response({input_tokens: tokens, output_tokens: 1, output_per_second: 1, partial: false})));
  const view = (revision: number) => <I18nProvider><SessionUsage provider={provider} threadID="session" revision={revision} running /></I18nProvider>;
  const rendered = render(view(0));
  expect(await screen.findByText("Input 10 tokens")).toBeInTheDocument();
  tokens = 20; vi.setSystemTime(Date.now() + 2000); rendered.rerender(view(1));
  expect(await screen.findByText("Input 20 tokens")).toBeInTheDocument();
});

test("collaboration results update through the source session revision", async () => {
  localStorage.setItem("wio_language", "en");
  let title = "Preparing child";
  vi.stubGlobal("fetch", vi.fn(async () => response({supported: true, grant: {enabled: true}, requests: [], links: [{source_key: "codex:source", target_key: "claude:child", missing: false, session: {engine: "claude", id: "child", title, status: "completed", workspace_id: "workspace", url: "/?view=claude&thread=child"}}]})));
  const view = (revision: number) => <I18nProvider><SessionCollaboration engine="codex" id="source" workspaceID="workspace" projectID="project" revision={revision} /></I18nProvider>;
  const rendered = render(view(0));
  fireEvent.click(await screen.findByRole("button", {name: /Session collaboration/}));
  expect(await screen.findByRole("link", {name: /Preparing child/})).toBeInTheDocument();
  title = "Finished child"; rendered.rerender(view(1));
  expect(await screen.findByRole("link", {name: /Finished child/})).toBeInTheDocument();
});

test("workspace results update an outstanding file scan through the page revision", async () => {
  localStorage.setItem("wio_language", "en");
  let finished = false;
  vi.stubGlobal("fetch", vi.fn(async () => response({workspace_id: "workspace", files: [], status: finished ? "failed" : "scanning", error: finished ? "Scan result received" : ""})));
  const view = (revision: number) => <I18nProvider><WorkspaceFilesPanel workspaceID="workspace" taskID="task" taskStatus="idle" realtime={revision} notify={vi.fn()} activePath="" activeMode="file" onOpenFile={vi.fn()} /></I18nProvider>;
  const rendered = render(view(0));
  expect(await screen.findByText("Scanning project files")).toBeInTheDocument();
  finished = true; rendered.rerender(view(1));
  expect(await screen.findByText("Scan result received")).toBeInTheDocument();
  expect(screen.getByTitle("Refresh project files")).toBeEnabled();
});
