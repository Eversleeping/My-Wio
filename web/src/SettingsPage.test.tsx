import { DataProvider } from "./data/resources";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { SettingsPage } from "./pages/SettingsPage";
import { I18nProvider } from "./i18n";
import type { SettingsSection } from "./navigation";

const scheduledTask = {
  id: "scheduled-1", thread_id: "thread-1", thread_title: "Review releases", workspace_id: "workspace-1", project_id: "project-1", project_name: "Console", server_id: "server-1", server_name: "production", server_status: "online", codex_thread_id: "codex-thread-1", workspace_path: "/srv/console",
  name: "Nightly release review", prompt: "Check the release queue", schedule: "0 2 * * *", timezone: "UTC", enabled: true, model: "", reasoning_effort: "", approval_mode: "on-request", next_run_at: "2026-08-03T02:00:00Z", last_run_at: null, last_run_status: "", last_run_message: "", last_operation_id: "", created_at: "2026-08-02T00:00:00Z", updated_at: "2026-08-02T00:00:00Z"
};

const profile = { id: "profile-1", kind: "codex" as const, name: "Primary Codex", endpoint: "https://api.example.test/v1", username: "", model: "gpt-5", commit_name: "", commit_email: "", updated_at: "2026-08-02T00:00:00Z" };
const secretSet = { id: "secret-set-1", name: "Production secrets", updated_at: "2026-08-02T00:00:00Z" };

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function installSettingsFetch(onDelete?: (url: string) => Promise<Response> | Response, pagedSessions = false) {
  const requests: Array<{ url: string; method: string }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    requests.push({ url, method });
    if (method === "DELETE") return onDelete?.(url) ?? jsonResponse({});
    if (url === "/api/settings/collaboration") return jsonResponse({ enabled: false, target_modes: {} });
    if (url === "/api/settings/codex-cli") return jsonResponse({ target_version: "1.0.0", versions: ["1.0.0"] });
    if (url === "/api/settings/gemini-cli") return jsonResponse({ target_version: "0.65.0", versions: ["0.65.0"] });
    if (url === "/api/settings/claude-cli") return jsonResponse({ target_version: "2.1.318", versions: ["2.1.318"] });
    if (url === "/api/servers") return jsonResponse([]);
    if (url === "/api/credential-profiles") return jsonResponse([profile]);
    if (url === "/api/secret-sets") return jsonResponse([secretSet]);
    if (url.startsWith("/api/audit?")) return jsonResponse({ items: [], has_more: false, next: null });
    if (url === "/api/scheduled-tasks") return jsonResponse([scheduledTask]);
    if (url.startsWith("/api/sessions?")) {
      const tail = new URL(url,"http://localhost").searchParams.has("cursor");
      return jsonResponse({items: pagedSessions ? [{id:tail?"late":"early",engine:tail?"claude":"codex",title:tail?"Later Claude session":"First Codex session",project_name:"Console",server_name:"Server"}] : [], next_cursor:pagedSessions && !tail ? "next-page" : null, groups:[]});
    }
    return jsonResponse({ error: `Unexpected request: ${method} ${url}` }, 404);
  }));
  return requests;
}

const revisions = {};

function renderSettings() {
  return render(<I18nProvider><DataProvider revisions={revisions}><SettingsPage notify={vi.fn()} /></DataProvider></I18nProvider>);
}

/** Renders the page the way the application does: the owner of the address bar chooses the category. */
function renderOwnedSettings(section: SettingsSection | undefined, onSectionChange = vi.fn()) {
  const page = (next: SettingsSection | undefined) => <I18nProvider><DataProvider revisions={revisions}><SettingsPage notify={vi.fn()} section={next} onSectionChange={onSectionChange} /></DataProvider></I18nProvider>;
  const view = render(page(section));
  return { onSectionChange, show: (next: SettingsSection | undefined) => view.rerender(page(next)) };
}

const category = (name: string) => screen.getByRole("link", { name });

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

test("requires explicit confirmation before deleting settings resources", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests = installSettingsFetch();
  const user = userEvent.setup();
  renderSettings();
  await user.click(category("Scheduled tasks"));
  await screen.findByText(scheduledTask.name);

  await user.click(screen.getByRole("button", { name: "Delete scheduled task" }));
  const scheduledConfirmation = await screen.findByRole("dialog", { name: "Delete scheduled task" });
  expect(within(scheduledConfirmation).getByRole("alert")).toHaveTextContent("future automatic runs will stop permanently");
  expect(requests.some(request => request.method === "DELETE")).toBe(false);
  await user.click(within(scheduledConfirmation).getByRole("button", { name: "Delete scheduled task" }));
  await waitFor(() => expect(requests).toContainEqual({ url: `/api/scheduled-tasks/${scheduledTask.id}`, method: "DELETE" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete scheduled task" })).not.toBeInTheDocument());

  await user.click(category("Credentials & secrets"));
  await user.click(await screen.findByRole("button", { name: "Delete credential profile" }));
  const profileConfirmation = await screen.findByRole("dialog", { name: "Delete credential profile" });
  expect(within(profileConfirmation).getByRole("alert")).toHaveTextContent("Installed servers will keep their current local copy");
  await user.click(within(profileConfirmation).getByRole("button", { name: "Delete credential profile" }));
  await waitFor(() => expect(requests).toContainEqual({ url: `/api/credential-profiles/${profile.id}`, method: "DELETE" }));

  await user.click(screen.getByRole("button", { name: "Delete secret set" }));
  const secretConfirmation = await screen.findByRole("dialog", { name: "Delete secret set" });
  expect(within(secretConfirmation).getByRole("alert")).toHaveTextContent("encrypted values will be permanently removed");
  await user.click(within(secretConfirmation).getByRole("button", { name: "Delete secret set" }));
  await waitFor(() => expect(requests).toContainEqual({ url: `/api/secret-sets/${secretSet.id}`, method: "DELETE" }));
});

test("keeps a failed settings deletion available for retry and blocks duplicate submissions while busy", async () => {
  window.localStorage.setItem("wio_language", "en");
  let attempts = 0;
  let resolveFirstDelete: ((response: Response) => void) | undefined;
  const requests = installSettingsFetch(url => {
    attempts += 1;
    if (attempts === 1) return new Promise<Response>(resolve => { resolveFirstDelete = resolve; });
    return Promise.resolve(jsonResponse({ url }));
  });
  const user = userEvent.setup();
  renderSettings();
  await user.click(category("Credentials & secrets"));
  await screen.findByText(profile.name);

  await user.click(screen.getByRole("button", { name: "Delete credential profile" }));
  const confirmation = await screen.findByRole("dialog", { name: "Delete credential profile" });
  const confirm = within(confirmation).getByRole("button", { name: "Delete credential profile" });
  await user.click(confirm);
  await waitFor(() => expect(resolveFirstDelete).toBeDefined());
  expect(confirm).toBeDisabled();
  expect(within(confirmation).getByRole("button", { name: "Cancel" })).toBeDisabled();
  expect(within(confirmation).getByRole("button", { name: "Close" })).toBeDisabled();
  await user.click(confirm);
  expect(requests.filter(request => request.method === "DELETE")).toHaveLength(1);

  resolveFirstDelete?.(jsonResponse({ error: "profile still in use" }, 409));
  await waitFor(() => expect(within(confirmation).getByRole("button", { name: "Delete credential profile" })).toBeEnabled());
  expect(screen.getByRole("dialog", { name: "Delete credential profile" })).toBeInTheDocument();

  await user.click(within(confirmation).getByRole("button", { name: "Delete credential profile" }));
  await waitFor(() => expect(requests.filter(request => request.method === "DELETE")).toHaveLength(2));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete credential profile" })).not.toBeInTheDocument());
});


test("loads settings session choices from every unified cursor page", async () => {
 window.localStorage.setItem("wio_language","en");
 const requests=installSettingsFetch(undefined,true), user=userEvent.setup();
 renderSettings();
 await user.click(category("Scheduled tasks"));
 await screen.findByText(scheduledTask.name);
 await user.click(screen.getByRole("button",{name:"New scheduled task"}));
 const dialog=await screen.findByRole("dialog",{name:"New scheduled task"});
 await user.selectOptions(within(dialog).getByLabelText("Agent"),"claude");
 await within(dialog).findByRole("option",{name:/Later Claude session/});
 expect(requests.some(request=>request.url.includes("cursor=next-page"))).toBe(true);
 expect(requests.some(request=>/\/api\/(?:threads|claude\/threads|gemini\/threads)/.test(request.url))).toBe(false);
});

test("shows one category at a time and loads each on its first visit", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests = installSettingsFetch();
  const user = userEvent.setup();
  renderSettings();
  const categories = within(screen.getByRole("navigation", { name: "Settings categories" }));
  expect(categories.getAllByRole("link").map(link => link.getAttribute("aria-label"))).toEqual(["Session collaboration", "Server runtime", "CLI versions", "Scheduled tasks", "Credentials & secrets"]);
  expect(category("Session collaboration")).toHaveAttribute("aria-current", "page");
  expect(category("Session collaboration")).toHaveAccessibleDescription("Child sessions and permissions");
  await screen.findByRole("checkbox", { name: "Allow creating and managing child sessions" });
  expect(new Set(requests.map(request => request.url))).toEqual(new Set(["/api/settings/collaboration"]));

  await user.click(category("Scheduled tasks"));
  await screen.findByText(scheduledTask.name);
  expect(category("Scheduled tasks")).toHaveAttribute("aria-current", "page");
  expect(category("Session collaboration")).not.toHaveAttribute("aria-current");
  expect(screen.queryByRole("checkbox", { name: "Allow creating and managing child sessions" })).not.toBeInTheDocument();
  expect(requests.some(request => request.url === "/api/credential-profiles" || request.url.startsWith("/api/settings/codex-cli"))).toBe(false);

  await user.click(category("CLI versions"));
  for (const name of ["Codex CLI", "Gemini CLI", "Claude CLI"]) {
    expect(await within(screen.getByRole("group", { name })).findByText(name === "Codex CLI" ? "1.0.0" : name === "Gemini CLI" ? "0.65.0" : "2.1.318")).toBeVisible();
  }
  expect(screen.queryByText(scheduledTask.name)).not.toBeVisible();
});

test("keeps unsaved edits of a category while another one is shown", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests = installSettingsFetch();
  const user = userEvent.setup();
  renderSettings();
  const enable = await screen.findByRole("checkbox", { name: "Allow creating and managing child sessions" });
  await waitFor(() => expect(enable).toBeEnabled());
  await user.click(enable);

  await user.click(category("Server runtime"));
  expect(await screen.findByRole("combobox", { name: "Select server" })).toBeVisible();
  await user.click(category("Session collaboration"));
  expect(screen.getByRole("checkbox", { name: "Allow creating and managing child sessions" })).toBeChecked();
  expect(requests.filter(request => request.url === "/api/settings/collaboration")).toHaveLength(1);
});

test("opens the category chosen by its owner and leaves the next choice to it", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests = installSettingsFetch();
  const user = userEvent.setup();
  const { onSectionChange, show } = renderOwnedSettings("credentials");
  await screen.findByText(profile.name);
  expect(category("Credentials & secrets")).toHaveAttribute("aria-current", "page");
  expect(category("Credentials & secrets")).toHaveAttribute("href", "/?view=settings&section=credentials");
  expect(category("Session collaboration")).toHaveAttribute("href", "/?view=settings");
  expect(requests.some(request => request.url === "/api/settings/collaboration")).toBe(false);

  await user.click(category("Scheduled tasks"));
  expect(onSectionChange).toHaveBeenCalledExactlyOnceWith("scheduled-tasks");
  expect(category("Credentials & secrets")).toHaveAttribute("aria-current", "page");
  expect(requests.some(request => request.url === "/api/scheduled-tasks")).toBe(false);
  // Choosing the current category again is not a navigation.
  await user.click(category("Credentials & secrets"));
  expect(onSectionChange).toHaveBeenCalledTimes(1);

  show("scheduled-tasks");
  await screen.findByText(scheduledTask.name);
  expect(screen.getByText(profile.name)).not.toBeVisible();
  // Without a category in the address the first one is shown.
  show(undefined);
  expect(await screen.findByRole("checkbox", { name: "Allow creating and managing child sessions" })).toBeVisible();
  expect(category("Session collaboration")).toHaveAttribute("aria-current", "page");
});

test("closes the dialogs of a category that is no longer shown", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests = installSettingsFetch();
  const user = userEvent.setup();
  const { show } = renderOwnedSettings("scheduled-tasks");
  await screen.findByText(scheduledTask.name);
  await user.click(screen.getByRole("button", { name: "New scheduled task" }));
  await screen.findByRole("dialog", { name: "New scheduled task" });

  // The browser's Back button changes the category while the dialog is open.
  show("credentials");
  await screen.findByText(profile.name);
  await waitFor(() => expect(screen.queryByRole("dialog", { hidden: true })).not.toBeInTheDocument());
  await user.click(screen.getByRole("button", { name: "New credential profile" }));
  await screen.findByRole("dialog");

  show("scheduled-tasks");
  await waitFor(() => expect(screen.queryByRole("dialog", { hidden: true })).not.toBeInTheDocument());
  expect(screen.getByText(scheduledTask.name)).toBeVisible();

  show("credentials");
  await user.click(screen.getByRole("button", { name: "Delete secret set" }));
  await screen.findByRole("dialog", { name: "Delete secret set" });
  show(undefined);
  await waitFor(() => expect(screen.queryByRole("dialog", { hidden: true })).not.toBeInTheDocument());
  expect(requests.some(request => request.method !== "GET")).toBe(false);
});
