import { expect, test } from "@playwright/test";
import { installMockApplication, type MockAPIRequest } from "./support/mock-api";

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

function deploymentResponse(request: MockAPIRequest) {
  if (request.path === "/deployment-targets" && request.method === "GET") return { body: [deploymentTarget] };
  if (request.path === "/deployments" && request.method === "GET") return { body: [] };
  if (request.path === "/workspaces" && request.method === "GET") return { body: [] };
  if (request.path === "/servers" && request.method === "GET") return { body: [deploymentServer] };
  if (request.path === "/secret-sets" && request.method === "GET") return { body: [] };
  if (request.path === `/deployments/${queuedDeployment.id}` && request.method === "GET") return { body: deploymentDetail };
  return undefined;
}

test("an unconfigured installation can reach sign-in without a backend", async ({ page }) => {
  await installMockApplication(page, { configured: false });
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
  await installMockApplication(page, { configured: true });
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
  await installMockApplication(page, { configured: true });
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
