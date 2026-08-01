import type { Page } from "@playwright/test";

export type MockAPIRequest = {
  path: string;
  method: string;
  body: string | null;
};

export type MockAPIResponse = {
  status?: number;
  body: unknown;
};

type MockApplicationOptions = {
  configured: boolean;
  /**
   * Per-test API responses. Returning undefined delegates to the bootstrap
   * defaults below; requests without a matching response fail loudly.
   */
  onAPIRequest?: (request: MockAPIRequest) => MockAPIResponse | undefined | Promise<MockAPIResponse | undefined>;
};

const session = {
  username: "e2e-admin",
  csrf_token: "e2e-csrf-token",
  expires_at: "2099-01-01T00:00:00Z"
};

const summary = {
  counts: { online: 1, servers: 1, projects: 1, threads: 1, deployments: 0, alerts: 0 },
  deployments: [],
  alerts: []
};

function defaultResponse(path: string, method: string, configured: boolean): MockAPIResponse | undefined {
  if (path === "/health" && method === "GET") return { body: {} };
  if (path === "/setup/status" && method === "GET") return { body: { configured, auth_mode: "password" } };
  if (path === "/auth/session" && method === "GET") return { body: session };
  if (path === "/auth/login" && method === "POST") return { body: session };
  if (path === "/setup" && method === "POST") return { body: { username: "admin", auth_mode: "password" } };
  if (path === "/summary" && method === "GET") return { body: summary };
  if (path === "/approvals" && method === "GET") return { body: [] };
  if (path === "/settings/codex-cli" && method === "GET") return { body: { target_version: "1.0.0", versions: ["1.0.0"] } };
  return undefined;
}

/**
 * Serves the application bootstrap requests inside the browser. This keeps the
 * tests independent of a Go API, a running WebSocket service, and real users.
 */
export async function installMockApplication(page: Page, options: MockApplicationOptions) {
  await page.addInitScript(() => window.localStorage.setItem("wio_language", "en"));
  await page.routeWebSocket("**/api/ws", webSocket => {
    // Leave the client connection open. The app only listens for events, so no
    // traffic is needed to exercise authenticated navigation.
    webSocket.onMessage(() => {});
  });

  await page.route("**/api/**", async route => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api/, "");
    const method = request.method();

    const response = await options.onAPIRequest?.({ path, method, body: request.postData() ?? null })
      ?? defaultResponse(path, method, options.configured);
    if (!response) {
      await route.fulfill({
        status: 501,
        contentType: "application/json",
        body: JSON.stringify({ error: `Unmocked E2E API request: ${method} ${path}` })
      });
      return;
    }

    await route.fulfill({
      status: response.status ?? 200,
      contentType: "application/json",
      body: JSON.stringify(response.body)
    });
  });
}
