import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { SettingsPage } from "./App";
import { I18nProvider } from "./i18n";

const scheduledTask = {
  id: "scheduled-1", thread_id: "thread-1", thread_title: "Review releases", workspace_id: "workspace-1", project_id: "project-1", project_name: "Console", server_id: "server-1", server_name: "production", server_status: "online", codex_thread_id: "codex-thread-1", workspace_path: "/srv/console",
  name: "Nightly release review", prompt: "Check the release queue", schedule: "0 2 * * *", timezone: "UTC", enabled: true, model: "", reasoning_effort: "", approval_mode: "on-request", next_run_at: "2026-08-03T02:00:00Z", last_run_at: null, last_run_status: "", last_run_message: "", last_operation_id: "", created_at: "2026-08-02T00:00:00Z", updated_at: "2026-08-02T00:00:00Z"
};

const profile = { id: "profile-1", kind: "codex" as const, name: "Primary Codex", endpoint: "https://api.example.test/v1", username: "", model: "gpt-5", commit_name: "", commit_email: "", updated_at: "2026-08-02T00:00:00Z" };
const secretSet = { id: "secret-set-1", name: "Production secrets", updated_at: "2026-08-02T00:00:00Z" };

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function installSettingsFetch(onDelete?: (url: string) => Promise<Response> | Response) {
  const requests: Array<{ url: string; method: string }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    requests.push({ url, method });
    if (method === "DELETE") return onDelete?.(url) ?? jsonResponse({});
    if (url === "/api/settings/codex-cli") return jsonResponse({ target_version: "1.0.0", versions: ["1.0.0"] });
    if (url === "/api/credential-profiles") return jsonResponse([profile]);
    if (url === "/api/secret-sets") return jsonResponse([secretSet]);
    if (url.startsWith("/api/audit?")) return jsonResponse({ items: [], has_more: false, next: null });
    if (url === "/api/scheduled-tasks") return jsonResponse([scheduledTask]);
    if (url === "/api/threads") return jsonResponse([]);
    return jsonResponse({ error: `Unexpected request: ${method} ${url}` }, 404);
  }));
  return requests;
}

function renderSettings() {
  return render(<I18nProvider><SettingsPage realtime={0} notify={vi.fn()} /></I18nProvider>);
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

test("requires explicit confirmation before deleting settings resources", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests = installSettingsFetch();
  const user = userEvent.setup();
  renderSettings();
  await screen.findByText(scheduledTask.name);

  await user.click(screen.getByRole("button", { name: "Delete scheduled task" }));
  const scheduledConfirmation = await screen.findByRole("dialog", { name: "Delete scheduled task" });
  expect(within(scheduledConfirmation).getByRole("alert")).toHaveTextContent("future automatic runs will stop permanently");
  expect(requests.some(request => request.method === "DELETE")).toBe(false);
  await user.click(within(scheduledConfirmation).getByRole("button", { name: "Delete scheduled task" }));
  await waitFor(() => expect(requests).toContainEqual({ url: `/api/scheduled-tasks/${scheduledTask.id}`, method: "DELETE" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete scheduled task" })).not.toBeInTheDocument());

  await user.click(screen.getByRole("button", { name: "Delete credential profile" }));
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
