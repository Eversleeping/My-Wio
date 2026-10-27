import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { HiddenProjectsButton } from "./HiddenProjectsButton";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("keeps a project available for retry after restoration fails", async () => {
  window.localStorage.setItem("wio_language", "en");
  const notify = vi.fn(), onRestored = vi.fn();
  let restored = false, attempts = 0;
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => {
    if (init.method === "PATCH") {
      if (++attempts === 1) return new Response(JSON.stringify({ error: "Connection lost" }), { status: 503 });
      restored = true;
      return new Response("{}");
    }
    return new Response(JSON.stringify(restored ? [] : [{ project_id: "project", server_id: "tencent", project_name: "pic-web", server_name: "Tencent", hidden_at: "2026-10-28T00:00:00Z" }]));
  }));
  render(<I18nProvider><HiddenProjectsButton realtime={0} onRestored={onRestored} notify={notify} /></I18nProvider>);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Hidden projects" }));
  await user.click(await screen.findByRole("button", { name: "Show project" }));
  await waitFor(() => expect(notify).toHaveBeenCalledWith("Connection lost"));
  expect(onRestored).not.toHaveBeenCalled();
  expect(screen.getByText("Tencent")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Show project" }));
  expect(await screen.findByText("No hidden projects")).toBeInTheDocument();
  expect(onRestored).toHaveBeenCalledTimes(1);
});

test("retries a failed hidden-project list request", async () => {
  window.localStorage.setItem("wio_language", "en");
  let attempts = 0;
  vi.stubGlobal("fetch", vi.fn(async () => ++attempts === 1 ? new Response(JSON.stringify({ error: "Offline" }), { status: 503 }) : new Response("[]")));
  render(<I18nProvider><HiddenProjectsButton realtime={0} onRestored={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Hidden projects" }));
  expect(await screen.findByText("Offline")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("No hidden projects")).toBeInTheDocument();
});
