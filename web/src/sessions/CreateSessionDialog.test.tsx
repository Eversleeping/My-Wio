import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import type { Workspace } from "../types";
import { CreateSessionDialog } from "./CreateSessionDialog";
import type { AISession, Engine } from "./types";

const workspaces = ["one", "two", "pending"].map(id => ({ id, server_id: id, project_id: id, project_name: id, server_name: id, path: `/srv/${id}`, status: id === "pending" ? "cloning" : "ready" } as Workspace));
function setup(options: { initial?: string; currentSession?: AISession; unavailable?: string; failAvailability?: boolean; delayedCreate?: Promise<void>; delayedAvailability?: Promise<void> } = {}) {
  localStorage.setItem("wio_language", "en");
  const created: AISession[] = [], writes: { path: string; body: unknown }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit = {}) => {
    let body: unknown = {};
    const path = url.replace(/^\/api/, "");
    if (path.endsWith("/session-engines")) {
      if (options.delayedAvailability) await options.delayedAvailability;
      if (options.failAvailability) return new Response(JSON.stringify({error: "Availability failed"}), {status: 503});
      body = { workspace_id: path.split("/")[2], engines: ["codex", "gemini", "claude"].map(engine => ({ engine, available: engine !== options.unavailable, reason: engine === options.unavailable ? "credentials_missing" : "", capabilities: {}, defaults: { model: engine === "gemini" ? "flash" : "sonnet", mode: "default", effort: "" } })) };
    }
    else if (path === "/servers") body = workspaces.map(w => ({id: w.id, claude_profile_model: "sonnet", claude_effort_supported: true}));
    else if (path.endsWith("/gemini/models")) body = { configured: true, default_model: "flash", models: [{id: "flash", name: "flash"}] };
    else if (path.endsWith("/claude/models")) body = { configured: true, default_model: "sonnet", models: [{id: "sonnet", name: "sonnet"}] };
    else if (init.method === "POST" && /^\/sessions\/(codex|gemini|claude)$/.test(path)) {
      if (options.delayedCreate) await options.delayedCreate;
      const input = JSON.parse(String(init.body)); writes.push({path, body: input});
      body = {id: "new-session", workspace_id: input.workspace_id, ...input};
    }
    else throw new Error(`Unexpected create request: ${path}`);
    return new Response(JSON.stringify(body));
  }));
  render(<I18nProvider><CreateSessionDialog initialWorkspaceID={options.initial ?? "one"} currentSession={options.currentSession} workspaces={workspaces} onClose={() => undefined} onCreated={thread => created.push(thread)} /></I18nProvider>);
  return {user: userEvent.setup(), created, writes, dialog: screen.getByRole("dialog")};
}
afterEach(() => {vi.unstubAllGlobals(); vi.restoreAllMocks(); localStorage.clear();});

for (const engine of ["codex", "gemini", "claude"] as Engine[]) test(`creates ${engine} with one workspace selector and remembers the workspace's engine`, async () => {
  const {user, created, writes, dialog} = setup();
  await waitFor(() => expect(within(dialog).getByLabelText("Engine")).toBeEnabled());
  await user.selectOptions(within(dialog).getByLabelText("Engine"), engine);
  expect(within(dialog).getAllByLabelText("Workspace")).toHaveLength(1);
  expect(within(dialog).getAllByRole("combobox")).toHaveLength(2);
  expect(within(dialog).queryByRole("option", {name: /pending/})).not.toBeInTheDocument();
  const name = engine === "codex" ? "Create session" : `New ${engine === "gemini" ? "Gemini" : "Claude"} session`;
  await waitFor(() => expect(within(dialog).getByRole("button", {name})).toBeEnabled());
  await user.click(within(dialog).getByRole("button", {name}));
  await waitFor(() => expect(created).toHaveLength(1));
  expect(created[0]).toMatchObject({engine, id: "new-session", workspace_id: "one"});
  expect(writes[0]).toMatchObject({path: `/sessions/${engine}`, body: {workspace_id: "one"}});
  expect(localStorage.getItem("wio_workspace_engine:one")).toBe(engine);
});

test("uses each workspace's remembered engine and resets all unsaved native fields on workspace change", async () => {
  localStorage.setItem("wio_workspace_engine:one", "gemini");
  localStorage.setItem("wio_workspace_engine:two", "claude");
  const {user, dialog} = setup();
  await waitFor(() => expect(within(dialog).getByLabelText("Engine")).toHaveValue("gemini"));
  expect(within(dialog).queryByLabelText("Tool permissions")).not.toBeInTheDocument();
  await user.selectOptions(within(dialog).getByLabelText("Workspace"), "two");
  await waitFor(() => expect(within(dialog).getByLabelText("Engine")).toHaveValue("claude"));
  expect(within(dialog).queryByLabelText("Claude model or alias")).not.toBeInTheDocument();
  await user.selectOptions(within(dialog).getByLabelText("Workspace"), "one");
  await waitFor(() => expect(within(dialog).getByLabelText("Engine")).toHaveValue("gemini"));
  expect(within(dialog).getAllByRole("combobox")).toHaveLength(2);
});

test("keeps unavailable engines disabled with a backend reason and a settings link", async () => {
  localStorage.setItem("wio_workspace_engine:one", "claude");
  const {dialog, writes} = setup({unavailable: "claude"});
  expect(await within(dialog).findByRole("alert")).toHaveTextContent("Apply an API credential profile");
  expect(within(dialog).getByRole("option", {name: /Claude · Apply/})).toBeDisabled();
  expect(within(dialog).getByRole("link", {name: "Settings"})).toHaveAttribute("href", "/?view=settings&section=credentials");
  expect(within(dialog).queryByRole("button", {name: "New Claude session"})).not.toBeInTheDocument();
  expect(writes).toEqual([]);
});

test("does not mount a native form before availability loads and fails closed after an error", async () => {
  let release!: () => void;
  const pending = new Promise<void>(resolve => {release = resolve;});
  const {dialog, writes} = setup({delayedAvailability: pending, failAvailability: true});
  expect(within(dialog).getByLabelText("Engine")).toBeDisabled();
  expect(within(dialog).queryByRole("button", {name: "Create session"})).not.toBeInTheDocument();
  release();
  expect(await within(dialog).findByText("Availability failed")).toBeInTheDocument();
  expect(writes).toEqual([]);
});

test("locks the common selectors while a native creation is in flight", async () => {
  let release!: () => void;
  const pending = new Promise<void>(resolve => {release = resolve;});
  const {user, dialog, created} = setup({delayedCreate: pending});
  const submit = await within(dialog).findByRole("button", {name: "Create session"});
  await user.click(submit);
  expect(within(dialog).getByLabelText("Workspace")).toBeDisabled();
  expect(within(dialog).getByLabelText("Engine")).toBeDisabled();
  expect(submit).toBeDisabled();
  release();
  await waitFor(() => expect(created).toHaveLength(1));
  expect(within(dialog).getByLabelText("Workspace")).toBeEnabled();
});

for (const mode of ["auto_edit", "plan"]) test(`preserves selected Gemini defaults with ${mode} mode before workspace history`, async () => {
  localStorage.setItem("wio_workspace_engine:one", "gemini");
  const currentSession = {engine: "gemini", id: "selected", workspace_id: "one", model: "flash", approval_mode: mode, thinking_level: ""} as AISession;
  const {user, dialog, writes} = setup({currentSession});
  await waitFor(() => expect(within(dialog).getByRole("button", {name: "New Gemini session"})).toBeEnabled());
  const expectedMode = mode === "plan" ? "default" : mode;
  expect(within(dialog).queryByLabelText("Tool permissions")).not.toBeInTheDocument();
  await user.click(within(dialog).getByRole("button", {name: "New Gemini session"}));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body).toMatchObject({workspace_id: "one", model: "flash", approval_mode: expectedMode});
});
