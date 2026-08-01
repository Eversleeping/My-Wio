import type { Page } from "@playwright/test";

export type MockAPIRequest = {
  path: string;
  search: string;
  method: string;
  body: string | null;
};

export type MockAPIResponse = {
  status?: number;
} & (
  | { body: unknown; stream?: never }
  | { stream: unknown[]; body?: never }
);

export type MockApplication = {
  defaultAPIRequests: MockAPIRequest[];
  unexpectedAPIRequests: MockAPIRequest[];
  assertNoUnexpectedAPIRequests: () => void;
};

export type MockDefaultRequestContract = {
  path: string;
  method?: string;
  atLeast?: number;
  atMost?: number;
};

type MockApplicationOptions = {
  configured: boolean;
  /** Minimal bootstrap expectations checked automatically after each test. */
  expectedDefaultRequests?: MockDefaultRequestContract[];
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

const applications = new WeakMap<Page, MockApplication>();

function matchingRequests(requests: MockAPIRequest[], contract: MockDefaultRequestContract) {
  return requests.filter(request => request.path === contract.path && (!contract.method || request.method === contract.method));
}

/** Fails every test that installed a mock when any API request was not modeled. */
export function assertMockApplication(page: Page) {
  const application = applications.get(page);
  if (!application) throw new Error("E2E test did not install a page-level API mock");
  application.assertNoUnexpectedAPIRequests();
}

/**
 * Serves the application bootstrap requests inside the browser. This keeps the
 * tests independent of a Go API, a running WebSocket service, and real users.
 */
export async function installMockApplication(page: Page, options: MockApplicationOptions): Promise<MockApplication> {
  const defaultAPIRequests: MockAPIRequest[] = [];
  const unexpectedAPIRequests: MockAPIRequest[] = [];
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

    const mockRequest = { path, search: url.search, method, body: request.postData() ?? null };
    const customResponse = await options.onAPIRequest?.(mockRequest);
    const response = customResponse ?? defaultResponse(path, method, options.configured);
    if (!response) {
      unexpectedAPIRequests.push(mockRequest);
      await route.fulfill({
        status: 501,
        contentType: "application/json",
        body: JSON.stringify({ error: `Unmocked E2E API request: ${method} ${path}` })
      });
      return;
    }
    if (!customResponse) defaultAPIRequests.push(mockRequest);

    await route.fulfill({
      status: response.status ?? 200,
      contentType: "stream" in response ? "application/x-ndjson" : "application/json",
      body: "stream" in response ? response.stream.map(event => JSON.stringify(event)).join("\n") : JSON.stringify(response.body)
    });
  });

  const application = {
    defaultAPIRequests,
    unexpectedAPIRequests,
    assertNoUnexpectedAPIRequests: () => {
      if (unexpectedAPIRequests.length) throw new Error(`Unexpected E2E API request(s): ${unexpectedAPIRequests.map(request => `${request.method} ${request.path}`).join(", ")}`);
      for (const contract of options.expectedDefaultRequests ?? []) {
        const count = matchingRequests(defaultAPIRequests, contract).length;
        if (count < (contract.atLeast ?? 1)) throw new Error(`Expected default E2E API request was not made: ${contract.method ?? "*"} ${contract.path}`);
        if (contract.atMost !== undefined && count > contract.atMost) throw new Error(`Default E2E API request exceeded its contract: ${contract.method ?? "*"} ${contract.path} (${count})`);
      }
    }
  };
  applications.set(page, application);
  return application;
}
