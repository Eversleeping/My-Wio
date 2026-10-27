import { render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { MonitoringPage } from "./pages/MonitoringPage";
import { I18nProvider } from "./i18n";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

function renderMetrics(capacity: boolean) {
  window.localStorage.setItem("wio_language", "zh");
  const point = { server_id: "server-1", bucket_at: "2026-10-28T00:00:00Z", cpu_percent: 20, memory_percent: 43.4, disk_percent: 19.4, load_1: 1, net_rx_bytes: 0, net_tx_bytes: 0,
    ...(capacity ? { memory_used_bytes: 3.472 * 1024 ** 3, memory_total_bytes: 8 * 1024 ** 3, disk_used_bytes: 1.94 * 1024 ** 4, disk_total_bytes: 10 * 1024 ** 4 } : {}) };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const data = url === "/api/servers" ? [{ id: "server-1", name: "测试节点", status: "online" }] : url.includes("/metrics?") ? [point] : url.includes("/operation-metrics?") ? {} : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  return render(<I18nProvider><MonitoringPage realtime={0} /></I18nProvider>);
}

test("shows actual used/total capacity with binary units alongside utilization and peak", async () => {
  renderMetrics(true);
  const memory = await screen.findByText("3.5 / 8.0 GiB");
  const disk = await screen.findByText("1.9 / 10.0 TiB");
  expect(memory).toHaveAttribute("title", "已用 / 总量: 3.5 / 8.0 GiB");
  expect(within(memory.closest(".metric-stat") as HTMLElement).getByText("43.4% · 峰值 43.4%")).toBeInTheDocument();
  expect(within(disk.closest(".metric-stat") as HTMLElement).getByText("19.4% · 峰值 19.4%")).toBeInTheDocument();
});

test("keeps percentage metrics usable for historical samples without byte counts", async () => {
  renderMetrics(false);
  expect(await screen.findByText("43.4%")).toBeInTheDocument();
  expect(await screen.findByText("19.4%")).toBeInTheDocument();
  expect(screen.queryByText(/GiB|TiB/)).not.toBeInTheDocument();
});
