import { render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { formatUsageRate, SessionUsage } from "./SessionUsage";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test.each(["gemini", "codex"] as const)("shows %s session totals and an unavailable rate for incomplete history", async provider => {
  window.localStorage.setItem("wio_language", "en");
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ input_tokens: 1234567, output_tokens: 8910, output_per_second: null, partial: true }), { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  render(<I18nProvider><SessionUsage provider={provider} threadID="thread-1" revision={0} running={false} /></I18nProvider>);
  expect(await screen.findByText("Input 1,234,567 tokens")).toBeInTheDocument();
  expect(screen.getByText("Output 8,910 tokens")).toBeInTheDocument();
  expect(screen.getByText("Average output — tokens/sec")).toBeInTheDocument();
  expect(screen.getByText("Incomplete records")).toBeInTheDocument();
  expect(fetcher).toHaveBeenCalledWith(`/api/${provider === "gemini" ? "gemini/" : ""}threads/thread-1/usage`, expect.anything());
});

test.each(["gemini", "codex"] as const)("shows the %s average output rate per second", async provider => {
  window.localStorage.setItem("wio_language", "zh-CN");
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ input_tokens: 999999, output_tokens: 100, output_per_second: 2.345, partial: false }), { status: 200 })));
  render(<I18nProvider><SessionUsage provider={provider} threadID="thread-1" revision={0} running={false} /></I18nProvider>);
  expect(await screen.findByText("平均输出 2.35 tokens/秒")).toBeInTheDocument();
});

test("keeps small positive output rates visible", () => {
  expect(formatUsageRate(0.00123, "en")).toBe("0.0012");
  expect(formatUsageRate(0, "en")).toBe("0");
});
