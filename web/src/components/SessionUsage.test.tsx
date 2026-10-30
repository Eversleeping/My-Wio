import { render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { SessionUsage } from "./SessionUsage";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test.each(["gemini", "codex"] as const)("shows %s session totals and an unavailable rate for incomplete history", async provider => {
  window.localStorage.setItem("wio_language", "en");
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ input_tokens: 1234567, output_tokens: 8910, per_minute: null, partial: true }), { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  render(<I18nProvider><SessionUsage provider={provider} threadID="thread-1" revision={0} running={false} /></I18nProvider>);
  expect(await screen.findByText("Input 1,234,567 tokens")).toBeInTheDocument();
  expect(screen.getByText("Output 8,910 tokens")).toBeInTheDocument();
  expect(screen.getByText("Average — tokens/min")).toBeInTheDocument();
  expect(screen.getByText("Incomplete records")).toBeInTheDocument();
  expect(fetcher).toHaveBeenCalledWith(`/api/${provider === "gemini" ? "gemini/" : ""}threads/thread-1/usage`, expect.anything());
});
