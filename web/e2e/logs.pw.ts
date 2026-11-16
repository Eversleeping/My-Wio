import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));

test("logs switch and paginate operations, alerts and audit entries", async ({ page }, testInfo) => {
  const stamp = "2026-11-17T00:00:00Z";
  const requests: string[] = [];
  const acknowledged = new Set<string>();
  await installMockApplication(page, { configured: true, onAPIRequest: request => {
    const query = new URLSearchParams(request.search);
    const offset = Number(query.get("offset") ?? 0), limit = Number(query.get("limit") ?? 50);
    const indexes = Array.from({ length: 53 }, (_, i) => i).slice(offset, offset + limit);
    requests.push(request.path + request.search);
    if (request.path === "/operations") return { body: indexes.map(i => ({ id: `op-${i}`, server_name: "Node", kind: "inventory.scan", status: "succeeded", result: `Operation ${i}`, created_at: stamp, updated_at: stamp })) };
    if (request.path === "/alerts") return { body: indexes.map(i => ({ id: `alert-${i}`, title: `Alert ${i}`, severity: "warning", status: "open", server_name: "Node", opened_at: stamp, acknowledged_at: acknowledged.has(`alert-${i}`) ? stamp : null })) };
    if (request.path === "/audit") return { body: { items: indexes.map(i => ({ id: `audit-${i}`, action: `Audit ${i}`, resource_type: "server", resource_id: "", ip_address: "127.0.0.1", occurred_at: stamp })), has_more: offset + limit < 53, next: offset + limit < 53 ? offset + limit : null } };
    if (request.path.endsWith("/acknowledge")) { acknowledged.add(request.path.split("/")[2]); return { body: { ok: true } }; }
    return undefined;
  } });
  await page.goto("/?view=operations");
  await expect(page.getByRole("heading", { name: "Logs", exact: true })).toBeVisible();
  for (const [tab, prefix] of [["Agent operations", "Operation"], ["Alerts", "Alert"], ["Audit log", "Audit"]]) {
    await page.getByRole("tab", { name: tab, exact: true }).click();
    await expect(page.getByText(`${prefix} 0`, { exact: true })).toBeVisible();
    await expect(page.getByText(`${prefix} 50`, { exact: true })).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(page.getByText(`${prefix} 50`, { exact: true })).toBeVisible();
    await expect(page.getByText(`${prefix} 0`, { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Next page", exact: true })).toBeDisabled();
    await expect(page.getByText("Page 2 · 50 per page")).toBeVisible();
    for (const theme of ["dark", "light"]) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; document.body.dataset.theme = theme; }, theme);
      await page.screenshot({ path: testInfo.outputPath(`logs-${prefix.toLowerCase()}-${theme}.png`), fullPage: true, animations: "disabled" });
    }
  }
  await page.getByRole("tab", { name: "Alerts", exact: true }).click();
  await expect(page.getByText("Alert 50", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Acknowledge alert", exact: true }).first().click();
  await expect.poll(() => acknowledged.has("alert-50")).toBe(true);
  await expect(page.getByRole("button", { name: "Acknowledge alert", exact: true })).toHaveCount(2);
  await page.getByRole("button", { name: "Previous page", exact: true }).click();
  await expect(page.getByText("Alert 0", { exact: true })).toBeVisible();
  expect(requests).toContain("/operations?limit=51&offset=50");
  expect(requests).toContain("/alerts?limit=51&offset=50");
  expect(requests).toContain("/audit?limit=50&offset=50");
});
