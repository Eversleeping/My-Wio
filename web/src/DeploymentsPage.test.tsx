import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { DeploymentsPage } from "./App";
import { I18nProvider } from "./i18n";

const target = {
  id: "target-1", project_id: "project-1", server_id: "server-1", secret_set_id: "", environment: "production",
  repository: "https://example.com/project.git", git_ref: "main", compose_file: "compose.yaml", working_dir: "",
  build_mode: "build", health_checks: "[]", release_root: "/var/lib/wio-agent/releases", project_name: "project-management", server_name: "server-1"
};
const deployment = {
  id: "deployment-1", target_id: target.id, operation_id: "operation-1", commit_ref: "main", resolved_commit: "abc123456789",
  status: "succeeded", message: "deployment is healthy", project_name: target.project_name, environment: target.environment,
  created_at: "2026-07-21T10:00:00Z", started_at: "2026-07-21T10:00:01Z", finished_at: "2026-07-21T10:00:08Z"
};

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

test("edits a target, opens process logs, and deletes deployment history", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests: Array<{ url: string; method: string; body: string }> = [];
  vi.stubGlobal("confirm", vi.fn(() => true));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    requests.push({ url, method, body: String(init.body ?? "") });
    let payload: unknown = {};
    if (url === "/api/deployment-targets" && method === "GET") payload = [target];
    else if (url === `/api/deployment-targets/${target.id}` && method === "PUT") payload = { ...target, ...JSON.parse(String(init.body)) };
    else if (url === "/api/deployments" && method === "GET") payload = [deployment];
    else if (url === `/api/deployments/${deployment.id}` && method === "GET") payload = { deployment, events: [{ id: "event-1", deployment_id: deployment.id, status: "succeeded", message: "deployment is healthy", content: "clone output\ncompose output", occurred_at: "2026-07-21T10:00:08Z" }] };
    else if (url === `/api/deployments/${deployment.id}` && method === "DELETE") payload = { ok: true };
    else if (url === "/api/projects") payload = [{ id: "project-1", name: "project-management", remote_url: target.repository }];
    else if (url === "/api/servers") payload = [{ id: "server-1", name: "server-1" }];
    else if (url === "/api/secret-sets") payload = [];
    return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
  }));

  const user = userEvent.setup();
  render(<I18nProvider><DeploymentsPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  expect((await screen.findAllByText("project-management")).length).toBeGreaterThan(0);

  await user.click(screen.getByRole("button", { name: "Edit deployment target" }));
  const environment = screen.getByRole("textbox", { name: "Environment" });
  await user.clear(environment);
  await user.type(environment, "staging");
  await user.click(screen.getByRole("button", { name: "Save target" }));
  await waitFor(() => expect(requests.some(request => request.url === `/api/deployment-targets/${target.id}` && request.method === "PUT" && request.body.includes('"environment":"staging"'))).toBe(true));

  await user.click(screen.getByRole("button", { name: "View process logs" }));
  expect(await screen.findByText(/clone output/)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Close" }));

  await user.click(screen.getByRole("button", { name: "Delete deployment record" }));
  await waitFor(() => expect(requests.some(request => request.url === `/api/deployments/${deployment.id}` && request.method === "DELETE")).toBe(true));
});
