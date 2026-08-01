import type { Page } from "@playwright/test";

type MockApplicationOptions = {
  configured: boolean;
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

    let payload: unknown;
    if (path === "/setup/status") payload = { configured: options.configured, auth_mode: "password" };
    else if (path === "/auth/session") payload = session;
    else if (path === "/auth/login" && method === "POST") payload = session;
    else if (path === "/setup" && method === "POST") payload = { username: "admin", auth_mode: "password" };
    else if (path === "/summary") payload = summary;
    else if (path === "/settings/codex-cli") payload = { target_version: "1.0.0", versions: ["1.0.0"] };
    else payload = [];

    await route.fulfill({ contentType: "application/json", body: JSON.stringify(payload) });
  });
}
