import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { DeploymentsPage } from "./App";
import { I18nProvider } from "./i18n";

const target = {
  id: "target-1", project_id: "project-1", server_id: "server-1", secret_set_id: "", environment: "production",
  source_type: "remote" as const, workspace_id: "", workspace_path: "", workspace_name: "",
  repository: "https://example.com/project.git", git_ref: "main", compose_file: "compose.yaml", working_dir: "",
  build_mode: "build", health_checks: "[]", release_root: "/var/lib/wio-agent/releases", public_url: "http://203.0.113.10:5000", project_name: "project-management", server_name: "server-1",
  container_operation_id: "", container_action: "deploy", container_status: "running", container_message: "deployment is healthy", container_updated_at: "2026-07-21T10:00:08Z"
};
const deployment = {
  id: "deployment-1", target_id: target.id, operation_id: "operation-1", commit_ref: "main", resolved_commit: "abc123456789",
  status: "succeeded", message: "deployment is healthy", project_name: target.project_name, environment: target.environment, public_url: target.public_url,
  created_at: "2026-07-21T10:00:00Z", started_at: "2026-07-21T10:00:01Z", finished_at: "2026-07-21T10:00:08Z"
};

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

test("creates and edits public access settings, manages containers, and exposes deployment links", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests: Array<{ url: string; method: string; body: string }> = [];
  let detailEvents: unknown = [{ id: "event-1", deployment_id: deployment.id, status: "succeeded", message: "deployment is healthy", content: "clone output\ncompose output", occurred_at: "2026-07-21T10:00:08Z" }];
  vi.stubGlobal("confirm", vi.fn(() => true));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    requests.push({ url, method, body: String(init.body ?? "") });
    let payload: unknown = {};
    if (url === "/api/deployment-targets" && method === "GET") payload = [target];
    else if (url === "/api/deployment-targets" && method === "POST") payload = { ...target, id: "target-2", ...JSON.parse(String(init.body)) };
    else if (url === `/api/deployment-targets/${target.id}` && method === "PUT") payload = { ...target, ...JSON.parse(String(init.body)) };
    else if (url === `/api/deployment-targets/${target.id}/container` && method === "POST") payload = { operation_id: "container-operation-1", action: "stop" };
    else if (url === "/api/deployments" && method === "GET") payload = [deployment];
    else if (url === `/api/deployments/${deployment.id}` && method === "GET") payload = { deployment, events: detailEvents };
    else if (url === `/api/deployments/${deployment.id}` && method === "DELETE") payload = { ok: true };
    else if (url === "/api/workspaces") payload = [{ id: "workspace-1", project_id: "project-1", server_id: "server-1", path: "/srv/project", display_name: "project", status: "ready", branch: "main", project_name: "project-management" }];
    else if (url === "/api/servers") payload = [{ id: "server-1", name: "server-1", status: "online" }];
    else if (url === "/api/secret-sets") payload = [];
    return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
  }));

  const user = userEvent.setup();
  render(<I18nProvider><DeploymentsPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  expect((await screen.findAllByText("project-management")).length).toBeGreaterThan(0);
  expect(screen.getByRole("button", { name: "Deploy" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Stop containers" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Restart containers" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Remove containers" })).toBeInTheDocument();
  expect(screen.getAllByRole("link", { name: "203.0.113.10:5000" })).toHaveLength(2);

  await user.click(screen.getByRole("button", { name: "New target" }));
  await user.click(screen.getByRole("button", { name: "Import from remote repository" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "Server" }), "server-1");
  await user.type(screen.getByRole("textbox", { name: "Repository" }), "https://example.com/new-service.git");
  await user.type(screen.getByRole("textbox", { name: "External access URL" }), "http://198.51.100.20:8080");
  await user.click(screen.getByRole("button", { name: "Create target" }));
  await waitFor(() => expect(requests.some(request => request.url === "/api/deployment-targets" && request.method === "POST" && request.body.includes('"public_url":"http://198.51.100.20:8080"'))).toBe(true));

  await user.click(screen.getByRole("button", { name: "Stop containers" }));
  await waitFor(() => expect(requests.some(request => request.url === `/api/deployment-targets/${target.id}/container` && request.method === "POST" && request.body.includes('"action":"stop"'))).toBe(true));

  await user.click(screen.getByRole("button", { name: "More deployment actions" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit deployment target" }));
  expect(screen.queryByRole("textbox", { name: "Working directory" })).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox", { name: "Release root" })).not.toBeInTheDocument();
  expect(screen.getByText(/Before deployment, Wio checks Linux/)).toBeInTheDocument();
  const publicURL = screen.getByRole("textbox", { name: "External access URL" });
  expect(publicURL).toHaveValue(target.public_url);
  await user.clear(publicURL);
  await user.type(publicURL, "https://app.example.com");
  const environment = screen.getByRole("textbox", { name: "Environment" });
  await user.clear(environment);
  await user.type(environment, "staging");
  await user.click(screen.getByRole("button", { name: "Save target" }));
  await waitFor(() => expect(requests.some(request => request.url === `/api/deployment-targets/${target.id}` && request.method === "PUT" && request.body.includes('"environment":"staging"') && request.body.includes('"public_url":"https://app.example.com"'))).toBe(true));

  await user.click(screen.getByRole("button", { name: "View process logs" }));
  const detailDialog = await screen.findByRole("dialog", { name: "Deployment process logs" });
  expect(await within(detailDialog).findByText(/clone output/)).toBeInTheDocument();
  expect(within(detailDialog).getByRole("link", { name: "203.0.113.10:5000" })).toHaveAttribute("href", target.public_url);
  await user.click(within(detailDialog).getByRole("button", { name: "Close" }));

  detailEvents = null;
  await user.click(screen.getByRole("button", { name: "View process logs" }));
  expect(await screen.findByText("No process logs were recorded")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Close" }));

  await user.click(screen.getByRole("button", { name: "Delete deployment record" }));
  await waitFor(() => expect(requests.some(request => request.url === `/api/deployments/${deployment.id}` && request.method === "DELETE")).toBe(true));
});
