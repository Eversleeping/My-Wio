import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication, type MockAPIRequest } from "./support/mock-api";

const authenticatedBootstrapContract = [
  { method: "GET", path: "/setup/status" },
  { method: "GET", path: "/auth/session" }
];

const setupBootstrapContract = [
  { method: "GET", path: "/setup/status" },
  { method: "POST", path: "/setup" },
  { method: "POST", path: "/auth/login" }
];

const deploymentTarget = {
  id: "target-1",
  project_id: "project-1",
  server_id: "server-1",
  source_type: "workspace",
  workspace_id: "workspace-1",
  secret_set_id: "",
  environment: "production",
  repository: "",
  git_ref: "main",
  compose_file: "compose.yaml",
  working_dir: "/srv/wio/demo-app",
  build_mode: "build",
  health_checks: "[]",
  release_root: "/srv/wio/releases/demo-app",
  public_url: "https://demo.example.test",
  configured_public_url: "https://demo.example.test",
  detected_public_url: "https://demo.example.test",
  project_name: "Demo app",
  server_name: "production-1",
  workspace_path: "/srv/wio/demo-app",
  workspace_name: "demo-app",
  container_operation_id: "",
  container_action: "",
  container_status: "running",
  container_message: "Healthy",
  container_updated_at: "2026-08-02T00:00:00Z"
};

const queuedDeployment = {
  id: "deployment-1",
  target_id: deploymentTarget.id,
  operation_id: "operation-1",
  commit_ref: "main",
  resolved_commit: "8f4c2d1a",
  status: "queued",
  message: "Waiting for Agent",
  project_name: deploymentTarget.project_name,
  environment: deploymentTarget.environment,
  public_url: deploymentTarget.public_url,
  created_at: "2026-08-02T00:00:00Z",
  started_at: null,
  finished_at: null
};

const deploymentDetail = {
  deployment: queuedDeployment,
  events: [{
    id: "event-1",
    deployment_id: queuedDeployment.id,
    status: "queued",
    message: "Compose build queued",
    content: "Waiting for production-1",
    occurred_at: "2026-08-02T00:00:00Z"
  }]
};

const successfulDeployment = {
  ...queuedDeployment,
  status: "succeeded",
  message: "Deployment is healthy",
  snapshot_available: true,
  started_at: "2026-08-02T00:00:02Z",
  finished_at: "2026-08-02T00:00:08Z"
};

const deploymentSnapshot = {
  deployment_id: successfulDeployment.id,
  target_id: deploymentTarget.id,
  project_id: deploymentTarget.project_id,
  project_name: deploymentTarget.project_name,
  source_type: "workspace",
  environment: "production",
  server_id: deploymentTarget.server_id,
  server_name: deploymentTarget.server_name,
  workspace_id: deploymentTarget.workspace_id,
  workspace_path: deploymentTarget.workspace_path,
  workspace_name: deploymentTarget.workspace_name,
  repository: "",
  git_ref: "main",
  resolved_commit: "8f4c2d1a",
  compose_file: "compose.yaml",
  working_dir: deploymentTarget.working_dir,
  build_mode: "build",
  release_root: deploymentTarget.release_root,
  configured_public_url: deploymentTarget.configured_public_url,
  detected_public_url: deploymentTarget.detected_public_url,
  health_checks: "[]",
  secret_set_id: "secret-set-1",
  secret_set_name: "E2E deploy secrets",
  secret_set_key_version: 3,
  secret_set_updated_at: "2026-08-02T00:00:00Z",
  rollback_of_deployment_id: "",
  created_at: "2026-08-02T00:00:08Z"
};

const deploymentReview = {
  target_id: deploymentTarget.id,
  current: {
    ...deploymentSnapshot,
    environment: "staging",
    git_ref: "release",
    compose_file: "deploy/compose.yaml",
    configured_public_url: "https://staging.demo.example.test"
  },
  last_successful: deploymentSnapshot,
  changes: [
    { field: "environment", previous: "production", current: "staging" },
    { field: "git_ref", previous: "main", current: "release" },
    { field: "compose_file", previous: "compose.yaml", current: "deploy/compose.yaml" }
  ],
  snapshot_available: true
};

const trustedDeploymentDetail = {
  deployment: { ...successfulDeployment, snapshot: deploymentSnapshot },
  events: [{
    id: "event-success-1",
    deployment_id: successfulDeployment.id,
    status: "succeeded",
    message: "Deployment is healthy",
    content: "Compose services are healthy",
    occurred_at: "2026-08-02T00:00:08Z"
  }]
};

const rollbackDeployment = {
  ...queuedDeployment,
  id: "deployment-rollback-1",
  operation_id: "operation-rollback-1",
  status: "queued",
  message: "Rollback is queued",
  snapshot_available: true
};

const rollbackDeploymentDetail = {
  deployment: rollbackDeployment,
  events: [{
    id: "event-rollback-1",
    deployment_id: rollbackDeployment.id,
    status: "queued",
    message: "Rollback queued",
    content: "Restoring the selected immutable release",
    occurred_at: "2026-08-02T00:00:10Z"
  }]
};

const deploymentServer = {
  id: deploymentTarget.server_id,
  name: deploymentTarget.server_name,
  hostname: "production-1.example.test",
  status: "online",
  agent_version: "1.0.0",
  agent_target_version: "1.0.0",
  agent_update_available: false,
  agent_update_supported: true,
  codex_version: "1.0.0",
  codex_ready: 1,
  codex_target_version: "1.0.0",
  codex_update_available: false,
  codex_update_supported: true,
  address: "",
  configuration: "",
  notes: "",
  codex_profile_id: "",
  codex_profile_name: "",
  git_profile_id: "",
  git_profile_name: "",
  last_seen_at: "2026-08-02T00:00:00Z",
  created_at: "2026-08-02T00:00:00Z"
};

const codexProfile = {
  id: "profile-codex-1",
  kind: "codex",
  name: "E2E Codex profile",
  endpoint: "https://api.example.test/v1",
  username: "",
  model: "gpt-5.5",
  commit_name: "",
  commit_email: "",
  updated_at: "2026-08-02T00:00:00Z"
};

const enrolledServer = {
  ...deploymentServer,
  id: "server-enrolled-1",
  name: "e2e-edge-1",
  hostname: "e2e-edge-1.example.test",
  address: "192.0.2.55",
  codex_profile_id: codexProfile.id,
  codex_profile_name: codexProfile.name
};

const workspaceProject = {
  id: "project-workspace-1",
  name: "Web console",
  description: "",
  remote_url: "",
  default_branch: "main",
  status: "ready",
  provision_error: "",
  updated_at: "2026-08-02T00:00:00Z",
  workspace_count: 1,
  import_status: "",
  import_message: "",
  import_server_id: "server-workspace-1",
  import_server_name: "workspace-host",
  import_operation_id: "",
  pinned_at: null,
  hidden_at: null,
  archived_at: null
};

const workspaceServer = {
  ...deploymentServer,
  id: "server-workspace-1",
  name: "workspace-host",
  hostname: "workspace-host.example.test"
};

const managedWorkspace = {
  id: "workspace-managed-1",
  project_id: workspaceProject.id,
  server_id: workspaceServer.id,
  path: "/srv/wio/web-console",
  display_name: "frontend-main",
  management_mode: "managed",
  status: "ready",
  branch: "main",
  commit_sha: "8f4c2d1a",
  dirty: 0,
  last_git_refresh_at: "2026-08-02T00:00:00Z",
  git_error: "",
  kind: "primary",
  parent_workspace_id: null,
  server_name: workspaceServer.name,
  project_name: workspaceProject.name
};

const operationSummaries = [
  {
    id: "operation-summary-queued",
    server_id: workspaceServer.id,
    server_name: workspaceServer.name,
    project_id: workspaceProject.id,
    project_name: workspaceProject.name,
    workspace_id: managedWorkspace.id,
    workspace_path: managedWorkspace.path,
    workspace_name: managedWorkspace.display_name,
    resource_type: "workspace",
    resource_id: managedWorkspace.id,
    kind: "git.workspace.refresh",
    status: "queued",
    result: "Waiting for Agent",
    created_at: "2026-08-02T00:00:00Z",
    updated_at: "2026-08-02T00:00:02Z",
    delivered_at: null,
    started_at: null,
    completed_at: null
  },
  {
    id: "operation-summary-failed",
    server_id: deploymentServer.id,
    server_name: deploymentServer.name,
    project_id: deploymentTarget.project_id,
    project_name: deploymentTarget.project_name,
    workspace_id: "",
    workspace_path: "",
    workspace_name: "",
    resource_type: "deployment",
    resource_id: deploymentTarget.id,
    kind: "deploy.rollback",
    status: "failed",
    result: "Agent unavailable",
    created_at: "2026-08-02T00:00:01Z",
    updated_at: "2026-08-02T00:00:05Z",
    delivered_at: "2026-08-02T00:00:02Z",
    started_at: "2026-08-02T00:00:03Z",
    completed_at: "2026-08-02T00:00:05Z"
  }
];

const filteredProject = {
  ...workspaceProject,
  id: "project-filtered",
  name: "Legacy API",
  status: "failed",
  provision_error: "Agent unavailable",
  workspace_count: 0,
  import_server_id: workspaceServer.id,
  import_server_name: workspaceServer.name
};

const filteredWorkspace = {
  ...managedWorkspace,
  id: "workspace-filtered",
  project_id: filteredProject.id,
  path: "/srv/wio/legacy-api",
  display_name: "legacy-main",
  dirty: 1,
  project_name: filteredProject.name
};

const codexThread = {
  id: "thread-codex-1",
  workspace_id: managedWorkspace.id,
  project_id: workspaceProject.id,
  codex_thread_id: "codex-thread-1",
  title: "Review deployment readiness",
  status: "idle",
  path: managedWorkspace.path,
  server_id: workspaceServer.id,
  server_name: workspaceServer.name,
  project_name: workspaceProject.name,
  created_at: "2026-08-02T00:00:00Z",
  updated_at: "2026-08-02T00:00:00Z",
  pinned_at: null,
  archived_at: null,
  project_pinned_at: null,
  project_hidden_at: null
};

const longCodexThreads = Array.from({ length: 55 }, (_, index) => ({
  ...codexThread,
  id: `thread-long-${index + 1}`,
  codex_thread_id: `codex-thread-long-${index + 1}`,
  title: `Long session ${String(index + 1).padStart(2, "0")}`,
  updated_at: `2026-08-02T00:${String(index).padStart(2, "0")}:00Z`
}));

const codexGoalSnapshot = {
  status: "succeeded",
  supported: true,
  reason: "",
  codex_version: "1.0.0",
  data: null,
  error: "",
  requested_at: null,
  updated_at: "2026-08-02T00:00:00Z"
};

const workspaceFilesSnapshot = {
  workspace_id: managedWorkspace.id,
  files: [],
  truncated: false,
  status: "succeeded",
  error: "",
  requested_at: "2026-08-02T00:00:00Z",
  updated_at: "2026-08-02T00:00:00Z"
};

const pendingApproval = {
  id: "approval-1",
  thread_id: codexThread.id,
  request_id: "request-1",
  kind: "command",
  detail: { command: "git status" },
  status: "pending",
  title: "Run git status",
  expires_at: "2099-01-01T00:00:00Z"
};

function deploymentResponse(request: MockAPIRequest) {
  if (request.path === "/deployment-targets" && request.method === "GET") return { body: [deploymentTarget] };
  if (request.path === "/deployments" && request.method === "GET") return { body: [] };
  if (request.path === "/workspaces" && request.method === "GET") return { body: [] };
  if (request.path === "/servers" && request.method === "GET") return { body: [deploymentServer] };
  if (request.path === "/secret-sets" && request.method === "GET") return { body: [] };
  if (request.path === `/deployments/${queuedDeployment.id}` && request.method === "GET") return { body: deploymentDetail };
  return undefined;
}

function deploymentHistoryResponse(request: MockAPIRequest) {
  if (request.path === "/deployment-targets" && request.method === "GET") return { body: [deploymentTarget] };
  if (request.path === "/deployments" && request.method === "GET") return { body: [successfulDeployment] };
  if (request.path === "/workspaces" && request.method === "GET") return { body: [] };
  if (request.path === "/servers" && request.method === "GET") return { body: [deploymentServer] };
  if (request.path === "/secret-sets" && request.method === "GET") return { body: [{ id: "secret-set-1", name: "E2E deploy secrets", updated_at: "2026-08-02T00:00:00Z" }] };
  if (request.path === `/deployment-targets/${deploymentTarget.id}/review` && request.method === "GET") return { body: deploymentReview };
  if (request.path === `/deployments/${successfulDeployment.id}` && request.method === "GET") return { body: trustedDeploymentDetail };
  if (request.path === `/deployments/${rollbackDeployment.id}` && request.method === "GET") return { body: rollbackDeploymentDetail };
  return undefined;
}

function primaryConsoleResponse(request: MockAPIRequest) {
  if (["/servers", "/credential-profiles", "/projects", "/workspaces"].includes(request.path) && request.method === "GET") return { body: [] };
  return undefined;
}

test.afterEach(({ page }, testInfo) => {
  if (testInfo.status !== "skipped") assertMockApplication(page);
});

test("an unconfigured installation can reach sign-in without a backend", async ({ page }) => {
  await installMockApplication(page, { configured: false, expectedDefaultRequests: setupBootstrapContract });
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Create administrator" })).toBeVisible();
  await page.getByRole("radio", { name: "Username + fixed password Use one password for every sign-in." }).check();
  await page.getByLabel("Password", { exact: true }).fill("safe-test-password");
  await page.getByLabel("Confirm password", { exact: true }).fill("safe-test-password");

  const setupRequest = page.waitForRequest(request =>
    request.method() === "POST" && new URL(request.url()).pathname === "/api/setup"
  );
  await page.getByRole("button", { name: "Create administrator" }).click();
  expect(JSON.parse((await setupRequest).postData() ?? "{}")).toMatchObject({
    username: "admin",
    auth_mode: "password"
  });

  await expect(page.getByRole("heading", { name: "Administrator ready" })).toBeVisible();
  await page.getByRole("button", { name: "Continue to sign in" }).click();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();

  await page.getByLabel("Password", { exact: true }).fill("safe-test-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
});

test("an authenticated user can navigate the primary console", async ({ page }) => {
  test.skip(test.info().project.name !== "desktop-chromium", "Primary sidebar navigation is covered at desktop width.");
  await installMockApplication(page, { configured: true, expectedDefaultRequests: authenticatedBootstrapContract, onAPIRequest: primaryConsoleResponse });
  await page.goto("/");

  const topbar = page.locator("header.topbar");
  await expect(topbar.getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
  const navigation = page.getByRole("navigation", { name: "Primary navigation" });
  await navigation.getByRole("button", { name: "Servers", exact: true }).click();
  await expect(page).toHaveURL(/\?view=servers$/);
  await expect(topbar.getByRole("heading", { name: "Servers", exact: true })).toBeVisible();

  await navigation.getByRole("button", { name: "Projects", exact: true }).click();
  await expect(page).toHaveURL(/\?view=projects$/);
  await expect(topbar.getByRole("heading", { name: "Projects", exact: true })).toBeVisible();
});

test("the 390px mobile project opens and closes its responsive navigation", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "mobile-chromium", "Responsive menu is covered by the mobile project.");
  await installMockApplication(page, { configured: true, expectedDefaultRequests: authenticatedBootstrapContract, onAPIRequest: primaryConsoleResponse });
  await page.goto("/");

  const sidebar = page.locator(".sidebar");
  await expect(sidebar).not.toHaveClass(/\bopen\b/);
  await page.getByRole("button", { name: "Open navigation" }).click();
  await expect(sidebar).toHaveClass(/\bopen\b/);

  await page.getByRole("navigation", { name: "Primary navigation" }).getByRole("button", { name: "Projects", exact: true }).click();
  await expect(page).toHaveURL(/\?view=projects$/);
  await expect(sidebar).not.toHaveClass(/\bopen\b/);
  await expect(page.locator("header.topbar").getByRole("heading", { name: "Projects", exact: true })).toBeVisible();
});

test("an authenticated user queues a deployment and opens its progress log", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Deployment workflow is covered at desktop width.");
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === `/deployment-targets/${deploymentTarget.id}/deploy` && request.method === "POST") return { body: { deployment: queuedDeployment } };
      return deploymentResponse(request);
    }
  });
  await page.goto("/");

  await page.getByRole("navigation", { name: "Primary navigation" }).getByRole("button", { name: "Deployments", exact: true }).click();
  await expect(page.locator("header.topbar").getByRole("heading", { name: "Deployments", exact: true })).toBeVisible();
  await expect(page.getByText("Demo app", { exact: true })).toBeVisible();

  const deployRequest = page.waitForRequest(request =>
    request.method() === "POST" && new URL(request.url()).pathname === `/api/deployment-targets/${deploymentTarget.id}/deploy`
  );
  await page.getByRole("button", { name: "Deploy", exact: true }).click();
  expect(JSON.parse((await deployRequest).postData() ?? "{}")).toEqual({ commit_ref: "main" });

  await expect(page.locator(".toast")).toContainText("Deployment queued");
  const logDialog = page.getByRole("dialog", { name: /Deployment.*logs/ });
  await expect(logDialog).toBeVisible();
  await expect(logDialog.getByText("Compose build queued", { exact: true })).toBeVisible();
});

test("a deployment retry recovers from a transient API failure", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Deployment error recovery is covered at desktop width.");
  let attempts = 0;
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === `/deployment-targets/${deploymentTarget.id}/deploy` && request.method === "POST") {
        attempts += 1;
        return attempts === 1
          ? { status: 503, body: { error: "Agent is temporarily unavailable" } }
          : { body: { deployment: queuedDeployment } };
      }
      return deploymentResponse(request);
    }
  });
  await page.goto("/?view=deployments");

  const deploy = page.getByRole("button", { name: "Deploy", exact: true });
  await expect(deploy).toBeEnabled();
  await deploy.click();
  await expect(page.locator(".toast")).toContainText("Agent is temporarily unavailable");
  await expect(deploy).toBeEnabled();

  await deploy.click();
  await expect(page.locator(".toast")).toContainText("Deployment queued");
  await expect(page.getByRole("dialog", { name: /Deployment.*logs/ })).toBeVisible();
  expect(attempts).toBe(2);
});

test("an administrator enrolls a server after verifying its SSH fingerprint", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Server enrollment is covered at desktop width.");
  let installed = false;
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === "/servers" && request.method === "GET") return { body: installed ? [enrolledServer] : [] };
      if (request.path === "/credential-profiles" && request.method === "GET") return { body: [codexProfile] };
      if (request.path === "/servers/ssh/probe" && request.method === "POST") return { body: { fingerprint: "SHA256:e2e-server-fingerprint", key_type: "ssh-ed25519" } };
      if (request.path === "/servers/ssh/bootstrap-stream" && request.method === "POST") {
        installed = true;
        return {
          stream: [
            { type: "progress", step: "starting" },
            { type: "progress", step: "installing_agent", current: 1, total: 2 },
            { type: "complete", step: "completed", result: { server_id: enrolledServer.id, hostname: enrolledServer.hostname, architecture: "amd64", warnings: [] } }
          ]
        };
      }
      return undefined;
    }
  });
  await page.goto("/?view=servers");

  await page.getByRole("button", { name: "Enroll server", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Enroll Linux server" });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Server name").fill(enrolledServer.name);
  await dialog.getByLabel("SSH host or IP").fill(enrolledServer.address);
  await dialog.getByLabel("SSH port").fill("2222");
  await dialog.getByLabel("SSH user").fill("ubuntu");
  await dialog.getByLabel("Authentication").selectOption("password");
  await dialog.getByLabel("SSH password").fill("e2e-password");
  await expect(dialog.getByLabel("Codex API profile")).toHaveValue(codexProfile.id);

  const probeRequest = page.waitForRequest(request => request.method() === "POST" && new URL(request.url()).pathname === "/api/servers/ssh/probe");
  await dialog.getByRole("button", { name: "Check fingerprint", exact: true }).click();
  expect(JSON.parse((await probeRequest).postData() ?? "{}")).toEqual({ host: enrolledServer.address, port: 2222 });
  await expect(dialog.getByText("SHA256:e2e-server-fingerprint", { exact: true })).toBeVisible();

  const installRequest = page.waitForRequest(request => request.method() === "POST" && new URL(request.url()).pathname === "/api/servers/ssh/bootstrap-stream");
  await dialog.getByRole("button", { name: "Confirm and install", exact: true }).click();
  expect(JSON.parse((await installRequest).postData() ?? "{}")).toMatchObject({
    name: enrolledServer.name,
    host: enrolledServer.address,
    port: 2222,
    user: "ubuntu",
    auth_method: "password",
    password: "e2e-password",
    host_key_fingerprint: "SHA256:e2e-server-fingerprint",
    codex_profile_id: codexProfile.id
  });

  await expect(dialog.getByRole("heading", { name: "Server added" })).toBeVisible();
  await expect(page.locator(".toast")).toContainText("Server added");
  const serverSection = page.locator("section.section").filter({ has: page.getByRole("heading", { name: "Registered servers" }) });
  await expect(serverSection.locator("tbody tr").filter({ hasText: enrolledServer.name })).toBeVisible();
});

test("an administrator renames a managed project workspace", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Workspace management is covered at desktop width.");
  let workspaceName = managedWorkspace.display_name;
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === "/projects" && request.method === "GET") return { body: [workspaceProject] };
      if (request.path === "/workspaces" && request.method === "GET") return { body: [{ ...managedWorkspace, display_name: workspaceName }] };
      if (request.path === "/servers" && request.method === "GET") return { body: [workspaceServer] };
      if (request.path === `/workspaces/${managedWorkspace.id}` && request.method === "PATCH") {
        workspaceName = String(JSON.parse(request.body ?? "{}").display_name ?? workspaceName);
        return { body: { ...managedWorkspace, display_name: workspaceName } };
      }
      return undefined;
    }
  });
  await page.goto("/?view=projects");

  const workspaceSection = page.locator("section.section").filter({ has: page.getByRole("heading", { name: "Workspaces" }) });
  const workspaceRow = workspaceSection.locator("tbody tr").filter({ hasText: managedWorkspace.path });
  await expect(workspaceRow).toBeVisible();
  await workspaceRow.getByTitle("Manage workspace").click();
  const dialog = page.getByRole("dialog", { name: /Manage workspace/ });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Workspace name").fill("web-console-main");

  const renameRequest = page.waitForRequest(request => request.method() === "PATCH" && new URL(request.url()).pathname === `/api/workspaces/${managedWorkspace.id}`);
  await dialog.getByRole("button", { name: "Save", exact: true }).click();
  expect(JSON.parse((await renameRequest).postData() ?? "{}")).toEqual({ display_name: "web-console-main" });

  await expect(page.locator(".toast")).toContainText("Workspace saved");
  await expect(dialog).not.toBeVisible();
  await workspaceRow.getByTitle("Manage workspace").click();
  await expect(dialog.getByLabel("Workspace name")).toHaveValue("web-console-main");
});

test("an authenticated user creates a Codex session in a workspace", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Codex session creation is covered at desktop width.");
  let sessionCreated = false;
  let threadListLoads = 0;
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === "/threads" && request.search === "?archived=true&limit=50" && request.method === "GET") return { body: { items: [], has_more: false, next: null } };
      if (request.path === "/threads" && request.search === "?limit=50" && request.method === "GET") {
        threadListLoads += 1;
        return { body: { items: sessionCreated ? [codexThread] : [], has_more: false, next: null } };
      }
      if (request.path === "/workspaces" && request.method === "GET") return { body: [managedWorkspace] };
      if (request.path === "/threads" && request.method === "POST") {
        sessionCreated = true;
        return { body: codexThread };
      }
      if (request.path === `/threads/${codexThread.id}/events` && request.method === "GET") return { body: [] };
      if (request.path === `/threads/${codexThread.id}/goal` && request.method === "GET") return { body: codexGoalSnapshot };
      if (request.path === `/workspaces/${managedWorkspace.id}/files` && request.method === "GET") return { body: workspaceFilesSnapshot };
      return undefined;
    }
  });
  await page.goto("/?view=codex");

  await page.getByTitle("New Codex session").click();
  const dialog = page.getByRole("dialog", { name: "New Codex session" });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Workspace").selectOption(managedWorkspace.id);

  const createRequest = page.waitForRequest(request =>
    request.method() === "POST" && new URL(request.url()).pathname === "/api/threads"
  );
  await dialog.getByRole("button", { name: "Create session", exact: true }).click();
  expect(JSON.parse((await createRequest).postData() ?? "{}")).toEqual({ workspace_id: managedWorkspace.id });

  await expect(page.locator(".toast")).toContainText("Codex session created");
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("heading", { name: codexThread.title, exact: true })).toBeVisible();
  await expect.poll(() => threadListLoads).toBeGreaterThanOrEqual(2);
});

test("an authenticated user approves a pending Codex request", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Codex approvals are covered at desktop width.");
  let approvalPending = true;
  let approvalLoads = 0;
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === "/threads" && request.search === "?archived=true&limit=50" && request.method === "GET") return { body: { items: [], has_more: false, next: null } };
      if (request.path === "/threads" && request.search === "?limit=50" && request.method === "GET") return { body: { items: [codexThread], has_more: false, next: null } };
      if (request.path === "/workspaces" && request.method === "GET") return { body: [managedWorkspace] };
      if (request.path === "/approvals" && request.method === "GET") {
        approvalLoads += 1;
        return { body: approvalPending ? [pendingApproval] : [] };
      }
      if (request.path === `/approvals/${pendingApproval.id}/decision` && request.method === "POST") {
        approvalPending = false;
        return { body: {} };
      }
      if (request.path === `/threads/${codexThread.id}/events` && request.method === "GET") return { body: [] };
      if (request.path === `/threads/${codexThread.id}/goal` && request.method === "GET") return { body: codexGoalSnapshot };
      if (request.path === `/workspaces/${managedWorkspace.id}/files` && request.method === "GET") return { body: workspaceFilesSnapshot };
      return undefined;
    }
  });
  await page.goto("/?view=codex");

  const dialog = page.getByRole("dialog", { name: "Pending approvals" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("Run git status", { exact: true })).toBeVisible();
  const approvalRequest = page.waitForRequest(request =>
    request.method() === "POST" && new URL(request.url()).pathname === `/api/approvals/${pendingApproval.id}/decision`
  );
  await dialog.getByRole("button", { name: "Approve once", exact: true }).click();
  expect(JSON.parse((await approvalRequest).postData() ?? "{}")).toEqual({ decision: "approved" });

  await expect(page.locator(".toast")).toContainText("Approval granted");
  await expect(dialog).not.toBeVisible();
  await expect.poll(() => approvalLoads).toBeGreaterThanOrEqual(2);
});

test("keeps a long Codex session list bounded and loads the next window", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Long-session pagination is covered at desktop width.");
  const listRequests: string[] = [];
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === "/threads" && request.search === "?archived=true&limit=50" && request.method === "GET") return { body: { items: [], has_more: false, next: null } };
      if (request.path === "/threads" && request.method === "GET") {
        listRequests.push(request.search);
        if (request.search === "?limit=50") return { body: { items: longCodexThreads.slice(0, 50), has_more: true, next: 50 } };
        if (request.search === "?limit=50&offset=50") return { body: { items: longCodexThreads.slice(50), has_more: false, next: null } };
      }
      if (request.path === "/workspaces" && request.method === "GET") return { body: [managedWorkspace] };
      if (request.path === `/threads/${longCodexThreads[0].id}/events` && request.method === "GET") return { body: [] };
      if (request.path === `/threads/${longCodexThreads[0].id}/goal` && request.method === "GET") return { body: codexGoalSnapshot };
      if (request.path === `/workspaces/${managedWorkspace.id}/files` && request.method === "GET") return { body: workspaceFilesSnapshot };
      return undefined;
    }
  });
  await page.goto("/?view=codex");

  await expect(page.locator(".thread-select")).toHaveCount(50);
  await expect(page.getByRole("button", { name: "Load more sessions", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Load more sessions", exact: true }).click();
  await expect(page.locator(".thread-select")).toHaveCount(55);
  await expect(page.getByText("Long session 55", { exact: true })).toBeVisible();
  expect(listRequests).toContain("?limit=50");
  expect(listRequests).toContain("?limit=50&offset=50");
});

test("the operation center filters recent Agent work and links resources", async ({ page }) => {
  const operationRequests: string[] = [];
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === "/operations" && request.method === "GET") {
        operationRequests.push(request.search);
        return { body: request.search.includes("status=failed") ? [operationSummaries[1]] : operationSummaries };
      }
      return undefined;
    }
  });
  await page.goto("/?view=operations");

  await expect(page.getByRole("heading", { name: "Agent operations", exact: true })).toBeVisible();
  await expect(page.getByText("Waiting for Agent", { exact: true })).toBeVisible();
  await expect(page.getByText("Agent unavailable", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Demo app" })).toHaveAttribute("href", "?view=deployments");
  await page.getByLabel("Filter by status").selectOption("failed");
  await expect(page.getByText("Agent unavailable", { exact: true })).toBeVisible();
  await expect(page.getByText("Waiting for Agent", { exact: true })).not.toBeVisible();
  expect(operationRequests.some(search => new URLSearchParams(search).get("status") === "failed")).toBe(true);
});

test("project and workspace lists retain independent server, status, path, and Git filters", async ({ page }) => {
  const projectRequests: string[] = [];
  const workspaceRequests: string[] = [];
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === "/servers" && request.method === "GET") return { body: [workspaceServer, deploymentServer] };
      if (request.path === "/projects" && request.method === "GET") {
        projectRequests.push(request.search);
        const params = new URLSearchParams(request.search);
        if (params.get("name") === "Web") return { body: [workspaceProject] };
        if (params.get("status") === "failed") return { body: [filteredProject] };
        return { body: [workspaceProject, filteredProject] };
      }
      if (request.path === "/workspaces" && request.method === "GET") {
        workspaceRequests.push(request.search);
        const params = new URLSearchParams(request.search);
        if (params.get("path") === filteredWorkspace.path || params.get("git_status") === "dirty") return { body: [filteredWorkspace] };
        return { body: [managedWorkspace, filteredWorkspace] };
      }
      return undefined;
    }
  });
  await page.goto("/?view=projects");

  const projectSection = page.locator("section.section").filter({ has: page.getByRole("heading", { name: "Projects", exact: true }) });
  const workspaceSection = page.locator("section.section").filter({ has: page.getByRole("heading", { name: "Workspaces", exact: true }) });
  await expect(projectSection.getByRole("button", { name: "Web console", exact: true })).toBeVisible();
  await expect(projectSection.getByRole("button", { name: "Legacy API", exact: true })).toBeVisible();

  await projectSection.getByLabel("Project name").fill("Web");
  await expect(page).toHaveURL(/project_name=Web/);
  await expect(projectSection.getByRole("button", { name: "Web console", exact: true })).toBeVisible();
  await expect(projectSection.getByRole("button", { name: "Legacy API", exact: true })).not.toBeVisible();

  await projectSection.getByRole("button", { name: "Clear filters", exact: true }).click();
  await expect(page).toHaveURL(/\?view=projects$/);
  await projectSection.getByLabel("Project status").selectOption("failed");
  await expect(projectSection.getByRole("button", { name: "Legacy API", exact: true })).toBeVisible();
  await expect(projectSection.getByRole("button", { name: "Web console", exact: true })).not.toBeVisible();

  await projectSection.getByRole("button", { name: "Clear filters", exact: true }).click();
  await workspaceSection.getByLabel("Workspace path").fill(filteredWorkspace.path);
  await expect(workspaceSection.getByText(filteredWorkspace.path, { exact: true })).toBeVisible();
  await expect(workspaceSection.getByText(managedWorkspace.path, { exact: true })).not.toBeVisible();
  await workspaceSection.getByLabel("Git status").selectOption("dirty");
  await expect(workspaceSection.getByText("Legacy API", { exact: true })).toBeVisible();
  await expect(workspaceSection.getByText("Web console", { exact: true })).not.toBeVisible();
  expect(projectRequests.some(search => new URLSearchParams(search).get("name") === "Web")).toBe(true);
  expect(projectRequests.some(search => new URLSearchParams(search).get("status") === "failed")).toBe(true);
  expect(workspaceRequests.some(search => new URLSearchParams(search).get("path") === filteredWorkspace.path)).toBe(true);
  expect(workspaceRequests.some(search => new URLSearchParams(search).get("git_status") === "dirty")).toBe(true);
});

test("deployment editing shows configuration changes without exposing secret values", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Deployment configuration review is covered at desktop width.");
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: deploymentHistoryResponse
  });
  await page.goto("/?view=deployments");

  const targetsSection = page.locator("section.section").filter({ has: page.getByRole("heading", { name: "Deployment targets", exact: true }) });
  await targetsSection.getByRole("button", { name: "More deployment actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Edit deployment target", exact: true }).click();

  const editDialog = page.getByRole("dialog", { name: "Edit deployment target" });
  await expect(editDialog.getByText("Configuration review", { exact: true })).toBeVisible();
  await expect(editDialog.getByText("deploy/compose.yaml", { exact: true }).first()).toBeVisible();
  await expect(editDialog.getByText("release", { exact: true }).first()).toBeVisible();
  await expect(editDialog.getByText(/Secret values are never shown\./)).toBeVisible();
  await editDialog.getByRole("button", { name: "Close", exact: true }).click();
  await targetsSection.getByRole("button", { name: "New target", exact: true }).click();
  const createDialog = page.getByRole("dialog", { name: "New deployment target" });
  await expect(createDialog.getByText("Configuration review", { exact: true })).not.toBeVisible();
});

test("deployment history shows immutable snapshots and can roll back a selected release", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Deployment history and rollback are covered at desktop width.");
  let rollbackRequest: MockAPIRequest | null = null;
  await installMockApplication(page, {
    configured: true,
    expectedDefaultRequests: authenticatedBootstrapContract,
    onAPIRequest: request => {
      if (request.path === `/deployment-targets/${deploymentTarget.id}/rollback` && request.method === "POST") {
        rollbackRequest = request;
        return { body: { deployment: rollbackDeployment } };
      }
      return deploymentHistoryResponse(request);
    }
  });
  await page.goto("/?view=deployments");

  const historySection = page.locator("section.section").filter({ has: page.getByRole("heading", { name: "Deployment history", exact: true }) });
  const historyRow = historySection.locator("tbody tr").filter({ hasText: deploymentTarget.project_name });
  await expect(historyRow).toBeVisible();
  await historyRow.getByRole("button", { name: "View process logs", exact: true }).click();

  const logDialog = page.getByRole("dialog", { name: "Deployment process logs" });
  await expect(logDialog.getByText("Immutable configuration snapshot", { exact: true })).toBeVisible();
  await expect(logDialog.getByText("E2E deploy secrets (key version 3)", { exact: true })).toBeVisible();
  await logDialog.getByRole("button", { name: "Close", exact: true }).click();

  await historyRow.getByRole("button", { name: "Rollback to this release", exact: true }).click();
  const confirmation = page.getByRole("dialog", { name: "Rollback to previous release" });
  await expect(confirmation).toContainText("8f4c2d1a");
  await confirmation.getByRole("button", { name: "Rollback to previous release", exact: true }).click();

  await expect.poll(() => rollbackRequest).not.toBeNull();
  expect(JSON.parse(rollbackRequest?.body ?? "{}")).toEqual({ deployment_id: successfulDeployment.id });
  await expect(page.locator(".toast")).toContainText("Rollback queued");
  await expect(page.getByRole("dialog", { name: "Deployment process logs" })).toBeVisible();
});
