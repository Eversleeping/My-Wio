import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { DataProvider } from "./data/resources";
import { I18nProvider } from "./i18n";
import LogsPage from "./pages/LogsPage";

const stamp = "2026-11-17T00:00:00Z";
function setup() {
  localStorage.setItem("wio_language", "en");
  const requests: string[] = [];
  const failures = new Set<string>();
  const acknowledged = new Set<string>();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), location.origin);
    requests.push(url.pathname + url.search);
    if (failures.has(url.pathname + url.search)) return new Response(JSON.stringify({ error: "Page unavailable" }), { status: 503 });
    const offset = Number(url.searchParams.get("offset") ?? 0);
    const limit = Number(url.searchParams.get("limit") ?? 50);
    const indexes = Array.from({ length: 53 }, (_, i) => i).slice(offset, offset + limit);
    let body: unknown;
    if (init?.method === "POST") { acknowledged.add(url.pathname.split("/")[3]); body = { ok: true }; }
    else if (url.pathname === "/api/operations") body = indexes.map(i => ({ id: `op-${i}`, server_name: "Node", kind: "inventory.scan", status: "succeeded", result: `Operation ${i}`, created_at: stamp, updated_at: stamp }));
    else if (url.pathname === "/api/alerts") body = indexes.map(i => ({ id: `alert-${i}`, title: `Alert ${i}`, severity: "warning", status: "open", server_name: "Node", opened_at: stamp, acknowledged_at: acknowledged.has(`alert-${i}`) ? stamp : null }));
    else body = { items: indexes.map(i => ({ id: `audit-${i}`, action: `Audit ${i}`, resource_type: "server", resource_id: "", ip_address: "127.0.0.1", occurred_at: stamp })), has_more: offset + limit < 53, next: offset + limit < 53 ? offset + limit : null };
    return new Response(JSON.stringify(body));
  }));
  render(<I18nProvider><DataProvider revisions={{}}><LogsPage /></DataProvider></I18nProvider>);
  return { requests, failures, user: userEvent.setup(), acknowledged };
}
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

test("paginates all three logs on the server, retains each tab's position, and resets filtered operations", async () => {
  const { requests, user } = setup();
  await screen.findByText("Operation 0");
  expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();
  expect(screen.queryByText("Operation 50")).not.toBeInTheDocument();
  expect(requests.every(path => path.startsWith("/api/operations?"))).toBe(true);
  for (const [tab, prefix] of [["Agent operations", "Operation"], ["Alerts", "Alert"], ["Audit log", "Audit"]]) {
    await user.click(screen.getByRole("tab", { name: tab }));
    await screen.findByText(`${prefix} 0`);
    await user.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByText(`${prefix} 50`);
    expect(screen.queryByText(`${prefix} 0`)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next page" })).toBeDisabled();
    expect(screen.getByText("Page 2 · 50 per page")).toBeInTheDocument();
  }
  expect(requests).toContain("/api/operations?limit=51&offset=50");
  expect(requests).toContain("/api/alerts?limit=51&offset=50");
  expect(requests).toContain("/api/audit?limit=50&offset=50");
  await user.click(screen.getByRole("tab", { name: "Agent operations" }));
  await screen.findByText("Operation 50");
  await user.selectOptions(screen.getByLabelText("Filter by status"), "failed");
  await waitFor(() => expect(requests).toContain("/api/operations?status=failed&limit=51&offset=0"));
  expect(screen.getByText("Page 1 · 50 per page")).toBeInTheDocument();
});

test("lets a failed page retry or go back without showing records from the previous page", async () => {
  const { failures, user } = setup();
  await screen.findByText("Operation 0");
  failures.add("/api/operations?limit=51&offset=50");
  await user.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByText("Page unavailable");
  expect(screen.queryByText("Operation 0")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Previous page" })).toBeEnabled();
  failures.clear();
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByText("Operation 50");
  await user.click(screen.getByRole("button", { name: "Previous page" }));
  await screen.findByText("Operation 0");
});

test("acknowledges an alert on the current page and exposes keyboard tab navigation", async () => {
  const { user, acknowledged } = setup();
  await screen.findByText("Operation 0");
  screen.getByRole("tab", { name: "Agent operations" }).focus();
  await user.keyboard("{ArrowRight}");
  expect(screen.getByRole("tab", { name: "Alerts" })).toHaveFocus();
  await screen.findByText("Alert 0");
  await user.click(screen.getAllByRole("button", { name: "Acknowledge alert" })[0]);
  await waitFor(() => expect(acknowledged.has("alert-0")).toBe(true));
  await waitFor(() => expect(screen.getAllByRole("button", { name: "Acknowledge alert" })).toHaveLength(49));
});
