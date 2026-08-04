import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { DeploymentsPage } from "./pages/DeploymentsPage";
import { I18nProvider } from "./i18n";

const target = {
  id: "target-1", project_id: "project-1", server_id: "server-1", secret_set_id: "", environment: "production",
  source_type: "remote" as const, workspace_id: "", workspace_path: "", workspace_name: "",
  repository: "https://example.com/project.git", git_ref: "main", compose_file: "compose.yaml", working_dir: "",
  build_mode: "build", health_checks: "[]", release_root: "/var/lib/wio-agent/releases", public_url: "http://203.0.113.10:5000", configured_public_url: "http://203.0.113.10:5000", detected_public_url: "", project_name: "project-management", server_name: "server-1",
  container_operation_id: "", container_action: "deploy", container_status: "running", container_message: "deployment is healthy", container_updated_at: "2026-07-21T10:00:08Z"
};
const server = { id: "server-1", name: "server-1", hostname: "server.example.test", address: "203.0.113.10", status: "online", exact_rollback_supported: true };
const deployment = {
  id: "deployment-1", target_id: target.id, operation_id: "operation-1", commit_ref: "main", resolved_commit: "abc123456789",
  status: "succeeded", message: "deployment is healthy", project_name: target.project_name, environment: target.environment, public_url: target.public_url, snapshot_available: true,
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
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    requests.push({ url, method, body: String(init.body ?? "") });
    let payload: unknown = {};
    if (url === "/api/deployment-targets" && method === "GET") payload = [target];
    else if (url === "/api/deployment-targets" && method === "POST") payload = { ...target, id: "target-2", ...JSON.parse(String(init.body)) };
    else if (url === `/api/deployment-targets/${target.id}` && method === "PUT") payload = { ...target, ...JSON.parse(String(init.body)) };
    else if (url === `/api/deployment-targets/${target.id}/review`) {
      const current = { project_id: target.project_id, project_name: target.project_name, source_type: target.source_type, environment: target.environment, server_id: target.server_id, server_name: target.server_name, workspace_id: "", workspace_path: "", workspace_name: "", repository: target.repository, git_ref: target.git_ref, resolved_commit: "", compose_file: target.compose_file, working_dir: target.working_dir, build_mode: target.build_mode, release_root: target.release_root, configured_public_url: target.configured_public_url, detected_public_url: "", health_checks: "[]", secret_set_id: "", secret_set_name: "", secret_set_key_version: 0, secret_set_updated_at: null };
      payload = { target_id: target.id, current, last_successful: { ...current, deployment_id: deployment.id, target_id: target.id, resolved_commit: deployment.resolved_commit, created_at: deployment.created_at }, changes: [], snapshot_available: true };
    }
    else if (url === `/api/deployment-targets/${target.id}/container` && method === "POST") payload = { operation_id: "container-operation-1", action: "stop" };
    else if (url === "/api/deployments" && method === "GET") payload = [deployment];
    else if (url === `/api/deployments/${deployment.id}` && method === "GET") payload = { deployment, events: detailEvents };
    else if (url === `/api/deployments/${deployment.id}` && method === "DELETE") payload = { ok: true };
    else if (url === "/api/workspaces") payload = [{ id: "workspace-1", project_id: "project-1", server_id: "server-1", path: "/srv/project", display_name: "project", status: "ready", branch: "main", project_name: "project-management" }];
    else if (url === "/api/servers") payload = [server];
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
  expect(screen.queryByText("Configuration review")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Import from remote repository" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "Server" }), "server-1");
  await user.type(screen.getByRole("textbox", { name: "Repository" }), "https://example.com/new-service.git");
  await user.type(screen.getByRole("spinbutton", { name: "Port" }), "8080");
  await user.click(screen.getByRole("button", { name: "Create target" }));
  await waitFor(() => expect(requests.some(request => request.url === "/api/deployment-targets" && request.method === "POST" && request.body.includes('"public_url":"http://203.0.113.10:8080"'))).toBe(true));

  await user.click(screen.getByRole("button", { name: "Stop containers" }));
  const stopConfirmation = await screen.findByRole("dialog", { name: "Stop containers" });
  expect(within(stopConfirmation).getByRole("alert")).toHaveTextContent("project-management production");
  expect(screen.getByRole("button", { name: "Deploy" })).toBeDisabled();
  expect(requests.some(request => request.url === `/api/deployment-targets/${target.id}/container` && request.method === "POST")).toBe(false);
  await user.click(within(stopConfirmation).getByRole("button", { name: "Stop containers" }));
  await waitFor(() => expect(requests.some(request => request.url === `/api/deployment-targets/${target.id}/container` && request.method === "POST" && request.body.includes('"action":"stop"'))).toBe(true));

  await user.click(screen.getByRole("button", { name: "More deployment actions" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit deployment target" }));
  expect(screen.queryByRole("textbox", { name: "Working directory" })).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox", { name: "Release root" })).not.toBeInTheDocument();
  expect(screen.getByText(/Before deployment, Wio checks Linux/)).toBeInTheDocument();
  const reviewSection = (await screen.findByText("Configuration review")).closest("section");
  expect(reviewSection).not.toBeNull();
  const accessMode = screen.getByRole("combobox", { name: "Access type" });
  expect(accessMode).toHaveValue("port");
  expect(screen.getByRole("spinbutton", { name: "Port" })).toHaveValue(5000);
  await user.selectOptions(accessMode, "domain");
  await user.type(screen.getByRole("textbox", { name: "Domain" }), "https://app.example.com");
  const environment = screen.getByRole("textbox", { name: "Environment" });
  await user.clear(environment);
  await user.type(environment, "staging");
  const composeFile = screen.getByRole("textbox", { name: "Compose file" });
  await user.clear(composeFile);
  await user.type(composeFile, "deploy/compose.yaml");
  expect(within(reviewSection!).getAllByText("staging").length).toBeGreaterThan(0);
  expect(within(reviewSection!).getAllByText("deploy/compose.yaml").length).toBeGreaterThan(0);
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
  const deleteHistoryDialog = await screen.findByRole("dialog", { name: "Delete deployment record" });
  await user.click(within(deleteHistoryDialog).getByRole("button", { name: "Delete deployment record" }));
  await waitFor(() => expect(requests.some(request => request.url === `/api/deployments/${deployment.id}` && request.method === "DELETE")).toBe(true));
});

test("queues destructive target cleanup with an explicit confirmation", async () => {
  window.localStorage.setItem("wio_language", "en");
  const notify = vi.fn();
  const requests: Array<{ url: string; method: string }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    requests.push({ url, method });
    let payload: unknown = [];
    if (url === "/api/deployment-targets" && method === "GET") payload = [target];
    else if (url === `/api/deployment-targets/${target.id}` && method === "DELETE") payload = { operation_id: "delete-operation-1", action: "delete" };
    else if (url === "/api/servers") payload = [server];
    return new Response(JSON.stringify(payload), { status: method === "DELETE" ? 202 : 200, headers: { "Content-Type": "application/json" } });
  }));

  const user = userEvent.setup();
  render(<I18nProvider><DeploymentsPage realtime={0} notify={notify} /></I18nProvider>);
  expect((await screen.findAllByText("project-management")).length).toBeGreaterThan(0);
  await user.click(screen.getByRole("button", { name: "More deployment actions" }));
  await user.click(screen.getByRole("menuitem", { name: "Delete deployment target" }));

  const confirmation = await screen.findByRole("dialog", { name: "Delete deployment target" });
  expect(within(confirmation).getByRole("alert")).toHaveTextContent("remove its containers, project volumes");
  expect(within(confirmation).getByRole("alert")).toHaveTextContent("project workspace is preserved");
  await user.click(within(confirmation).getByRole("button", { name: "Delete deployment target" }));
  await waitFor(() => expect(requests).toContainEqual({ url: `/api/deployment-targets/${target.id}`, method: "DELETE" }));
  expect(notify).toHaveBeenCalledWith("Deployment target deletion queued");
});

test("shows a detected public URL without treating it as a configured override", async () => {
  window.localStorage.setItem("wio_language", "en");
  const detectedTarget = { ...target, public_url: "http://203.0.113.10:5010", configured_public_url: "", detected_public_url: "http://203.0.113.10:5010" };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    let payload: unknown = [];
    if (url === "/api/deployment-targets") payload = [detectedTarget];
    else if (url === "/api/deployments") payload = [];
    else if (url === "/api/servers") payload = [server];
    return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
  }));

  const user = userEvent.setup();
  render(<I18nProvider><DeploymentsPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  expect(await screen.findByRole("link", { name: "203.0.113.10:5010" })).toHaveAttribute("href", detectedTarget.public_url);
  await user.click(screen.getByRole("button", { name: "More deployment actions" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit deployment target" }));
  expect(screen.getByRole("combobox", { name: "Access type" })).toHaveValue("port");
  expect((screen.getByRole("spinbutton", { name: "Port" }) as HTMLInputElement).value).toBe("");
});

test("rolls back to the exact historical deployment selected from the history", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests: Array<{ url: string; method: string; body: string }> = [];
  const historical = { ...deployment, snapshot_available: true };
  const rollbackDeployment = { ...deployment, id: "rollback-1", commit_ref: "rollback", status: "queued", snapshot_available: true };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    requests.push({ url, method, body: String(init.body ?? "") });
    let payload: unknown = [];
    if (url === "/api/deployment-targets") payload = [target];
    else if (url === "/api/deployments") payload = [historical];
    else if (url === "/api/deployments/rollback-1" && method === "GET") payload = { deployment: rollbackDeployment, events: [] };
    else if (url === "/api/deployment-targets/" + target.id + "/rollback" && method === "POST") payload = { deployment: rollbackDeployment, operation_id: "rollback-operation" };
    else if (url === "/api/servers") payload = [server];
    return new Response(JSON.stringify(payload), { status: method === "POST" ? 202 : 200, headers: { "Content-Type": "application/json" } });
  }));
  const user = userEvent.setup();
  render(<I18nProvider><DeploymentsPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  expect((await screen.findAllByText("project-management")).length).toBeGreaterThan(0);
  await user.click(screen.getByRole("button", { name: "Rollback to this release" }));
  const confirmation = await screen.findByRole("dialog", { name: /Rollback/ });
  expect(within(confirmation).getByRole("alert")).toHaveTextContent("abc123");
  await user.click(within(confirmation).getByRole("button", { name: /Rollback/ }));
  await waitFor(() => expect(requests.some(request => request.url === "/api/deployment-targets/" + target.id + "/rollback" && request.method === "POST" && request.body.includes('"deployment_id":"deployment-1"'))).toBe(true));
});

test("disables historical rollback for an Agent without exact release support", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    let payload: unknown = [];
    if (url === "/api/deployment-targets") payload = [target];
    else if (url === "/api/deployments") payload = [{ ...deployment, snapshot_available: true }];
    else if (url === "/api/servers") payload = [{ ...server, exact_rollback_supported: false }];
    return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  render(<I18nProvider><DeploymentsPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  const rollback = await screen.findByRole("button", { name: "Rollback to this release" });
  expect(rollback).toBeDisabled();
  expect(rollback).toHaveAttribute("title", "Upgrade the target Agent before rolling back to a historical release");
});
