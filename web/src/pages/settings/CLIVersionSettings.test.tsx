import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { DataProvider } from "../../data/resources";
import { I18nProvider } from "../../i18n";
import { CLIVersionSettings } from "./CLIVersionSettings";

type Engine = "codex" | "gemini" | "claude";
type Reply = { status?: number; body: unknown };

const revisions = {};
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });

/** Serves the three target versions; `replies` overrides one request, keyed as "METHOD url". */
function setup(replies: Record<string, () => Reply> = {}, initial: Partial<Record<Engine, { target_version: string; versions?: string[] }>> = {}) {
  window.localStorage.setItem("wio_language", "en");
  const targets: Record<Engine, { target_version: string; versions?: string[] }> = {
    codex: { target_version: "0.58.0", versions: ["0.58.0", "0.57.0"] },
    gemini: { target_version: "0.65.0", versions: ["0.65.0", "0.64.0"] },
    claude: { target_version: "2.1.318", versions: ["2.1.318", "2.1.317"] },
    ...initial
  };
  const requests: Array<{ url: string; method: string; body?: unknown }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input), method = init.method ?? "GET";
    requests.push({ url, method, ...(init.body ? { body: JSON.parse(String(init.body)) } : {}) });
    const reply = replies[`${method} ${url}`]?.();
    if (reply) return json(reply.body, reply.status);
    const engine = url.match(/^\/api\/settings\/(codex|gemini|claude)-cli(?:\/|$)/)?.[1] as Engine | undefined;
    if (!engine) return json({ error: `Unexpected request: ${method} ${url}` }, 404);
    if (url.endsWith("/select-version")) targets[engine] = { ...targets[engine], target_version: JSON.parse(String(init.body)).version };
    return json(targets[engine]);
  }));
  const notify = vi.fn();
  render(<I18nProvider><DataProvider revisions={revisions}><CLIVersionSettings notify={notify} /></DataProvider></I18nProvider>);
  const row = (engine: "Codex" | "Gemini" | "Claude") => within(screen.getByRole("group", { name: `${engine} CLI` }));
  return { user: userEvent.setup(), notify, requests, targets, row };
}

const optionLabels = (select: HTMLElement) => within(select).getAllByRole("option").map(option => option.textContent);

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("lists the three engines in one section and applies a version to one of them", async () => {
  const { user, notify, requests, row } = setup();
  expect(screen.getAllByRole("group").map(group => group.getAttribute("aria-label"))).toEqual(["Codex CLI", "Gemini CLI", "Claude CLI"]);
  const version = row("Codex").getByRole("combobox", { name: "Select a Codex CLI version" });
  await waitFor(() => expect(version).toHaveValue("0.58.0"));
  expect(optionLabels(version)).toEqual(["0.58.0 (latest)", "0.57.0"]);
  expect(row("Codex").getByText("0.58.0", { selector: "code" })).toBeVisible();
  const apply = row("Codex").getByRole("button", { name: "Apply version" });
  expect(apply).toBeDisabled();

  await user.selectOptions(version, "0.57.0");
  await user.click(apply);
  await waitFor(() => expect(notify).toHaveBeenCalledExactlyOnceWith("Codex CLI 0.57.0 is now the target version"));
  expect(requests).toContainEqual({ url: "/api/settings/codex-cli/select-version", method: "POST", body: { version: "0.57.0" } });
  // The saved target is read again, so the row no longer offers to apply it.
  expect(await row("Codex").findByText("0.57.0", { selector: "code" })).toBeVisible();
  await waitFor(() => expect(apply).toBeDisabled());
  expect(version).toHaveValue("0.57.0");

  await waitFor(() => expect(row("Gemini").getByRole("combobox", { name: "Select a Gemini CLI version" })).toHaveValue("0.65.0"));
  expect(row("Claude").getByRole("combobox", { name: "Select a Claude CLI version" })).toHaveValue("2.1.318");
  expect(requests.filter(request => request.method === "POST")).toHaveLength(1);
});

test("tells whether a Codex update check found a newer release", async () => {
  let checks = 0;
  const { user, notify, requests, row, targets } = setup({
    "POST /api/settings/codex-cli/check-updates": () => {
      checks += 1;
      if (checks === 1) targets.codex = { target_version: "0.59.0", versions: ["0.59.0", "0.58.0", "0.57.0"] };
      return { body: { ...targets.codex, latest_version: "0.59.0", updated: checks === 1 } };
    }
  });
  const check = row("Codex").getByRole("button", { name: "Check for updates" });
  await waitFor(() => expect(check).toBeEnabled());

  await user.click(check);
  await waitFor(() => expect(notify).toHaveBeenLastCalledWith("Codex CLI 0.59.0 is now the target version"));
  const version = row("Codex").getByRole("combobox", { name: "Select a Codex CLI version" });
  await waitFor(() => expect(version).toHaveValue("0.59.0"));
  expect(optionLabels(version)).toEqual(["0.59.0 (latest)", "0.58.0", "0.57.0"]);

  await waitFor(() => expect(check).toBeEnabled());
  await user.click(check);
  await waitFor(() => expect(notify).toHaveBeenLastCalledWith("Codex CLI 0.59.0 is already the latest stable release"));
  expect(requests.filter(request => request.method === "POST").map(request => request.url)).toEqual(["/api/settings/codex-cli/check-updates", "/api/settings/codex-cli/check-updates"]);
});

// The three blocks gave feedback differently before they became one list. The differences are
// kept as they were and are registered under "发现的问题" in docs/roadmap/README.md.
test("keeps the feedback each engine gave before the blocks were merged", async () => {
  const unreachable = () => ({ status: 502, body: { error: "registry unreachable" } });
  const { user, notify, row } = setup({
    "POST /api/settings/codex-cli/check-updates": unreachable,
    "POST /api/settings/gemini-cli/check-updates": unreachable,
    "POST /api/settings/claude-cli/check-updates": () => ({ body: { target_version: "2.1.318", versions: ["2.1.318", "2.1.317"], latest_version: "2.1.318", updated: false } })
  });
  for (const engine of ["Codex", "Gemini", "Claude"] as const) await waitFor(() => expect(row(engine).getByRole("button", { name: "Check for updates" })).toBeEnabled());

  // Codex reports a failure through the page notification only.
  await user.click(row("Codex").getByRole("button", { name: "Check for updates" }));
  await waitFor(() => expect(notify).toHaveBeenCalledExactlyOnceWith("registry unreachable"));
  expect(row("Codex").queryByRole("alert")).not.toBeInTheDocument();

  // Gemini and Claude report it in their own row.
  await user.click(row("Gemini").getByRole("button", { name: "Check for updates" }));
  expect(await row("Gemini").findByRole("alert")).toHaveTextContent("registry unreachable");
  expect(notify).toHaveBeenCalledTimes(1);
  expect(row("Gemini").getByRole("button", { name: "Check for updates" })).toBeEnabled();

  // A check that finds nothing new still reports the unchanged target as applied.
  await user.click(row("Claude").getByRole("button", { name: "Check for updates" }));
  await waitFor(() => expect(notify).toHaveBeenLastCalledWith("Claude CLI 2.1.318 is now the target version"));
  expect(row("Claude").queryByRole("alert")).not.toBeInTheDocument();
});

test("shows a target that could not be read and labels a lone target as each engine did", async () => {
  const { row } = setup(
    { "GET /api/settings/claude-cli": () => ({ status: 500, body: { error: "settings unavailable" } }) },
    { codex: { target_version: "0.58.0" }, gemini: { target_version: "0.65.0" } }
  );
  expect(await row("Claude").findByRole("alert")).toHaveTextContent("settings unavailable");
  expect(row("Claude").getByText("-", { selector: "code" })).toBeVisible();
  for (const name of ["Apply version", "Check for updates"]) expect(row("Claude").getByRole("button", { name })).toBeDisabled();
  expect(row("Claude").getByRole("combobox", { name: "Select a Claude CLI version" })).toBeDisabled();

  // Without a release list only the current target is offered. Codex still calls it the latest.
  const codex = row("Codex").getByRole("combobox", { name: "Select a Codex CLI version" });
  await waitFor(() => expect(codex).toHaveValue("0.58.0"));
  expect(optionLabels(codex)).toEqual(["0.58.0 (latest)"]);
  const gemini = row("Gemini").getByRole("combobox", { name: "Select a Gemini CLI version" });
  await waitFor(() => expect(gemini).toHaveValue("0.65.0"));
  expect(optionLabels(gemini)).toEqual(["0.65.0"]);
  expect(row("Codex").queryByRole("alert")).not.toBeInTheDocument();
  expect(row("Gemini").queryByRole("alert")).not.toBeInTheDocument();
});

test("does not report a Codex target that could not be read", async () => {
  const unavailable = () => ({ status: 500, body: { error: "settings unavailable" } });
  const { row } = setup({ "GET /api/settings/codex-cli": unavailable, "GET /api/settings/gemini-cli": unavailable });
  // The rows load together and Codex first, so once Gemini shows its failure Codex has failed too.
  expect(await row("Gemini").findByRole("alert")).toHaveTextContent("settings unavailable");
  expect(row("Codex").queryByRole("alert")).not.toBeInTheDocument();
  expect(row("Codex").getByText("-", { selector: "code" })).toBeVisible();
  for (const name of ["Apply version", "Check for updates"]) expect(row("Codex").getByRole("button", { name })).toBeDisabled();
  expect(row("Codex").getByRole("combobox", { name: "Select a Codex CLI version" })).toBeDisabled();
});
