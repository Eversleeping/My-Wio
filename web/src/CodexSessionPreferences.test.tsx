import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { SessionView } from "./App";
import { I18nProvider } from "./i18n";
import type { Thread } from "./types";

beforeEach(() => {
  window.localStorage.clear();
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn(async () => new Response("[]", { status: 200, headers: { "Content-Type": "application/json" } })));
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

function thread(id: string): Thread {
  return {
    id,
    workspace_id: `workspace-${id}`,
    project_id: "project-1",
    codex_thread_id: "",
    title: `Session ${id}`,
    status: "idle",
    path: `/srv/${id}`,
    server_id: "server-1",
    server_name: "Server",
    project_name: "Project",
    created_at: "2026-07-23T00:00:00Z",
    updated_at: "2026-07-23T00:00:00Z",
    pinned_at: null,
    archived_at: null,
    project_pinned_at: null,
    project_hidden_at: null
  };
}

function sessionView(value: Thread, realtime: unknown = 0) {
  return <I18nProvider><SessionView key={value.id} thread={value} approvals={[]} realtime={realtime} reloadApprovals={vi.fn()} notify={vi.fn()} onOpenFile={vi.fn()} onNewTask={vi.fn()} /></I18nProvider>;
}

function composerSelects() {
  return {
    approval: screen.getByLabelText("Approve on request"),
    model: screen.getByLabelText("Model override"),
    reasoning: screen.getByLabelText("Reasoning effort")
  };
}

test("keeps approval, model, and reasoning selections isolated by session", async () => {
  const user = userEvent.setup();
  const first = thread("first");
  const second = thread("second");
  const { rerender, unmount } = render(sessionView(first));

  let selects = composerSelects();
  await user.selectOptions(selects.approval, "never");
  await user.selectOptions(selects.model, "gpt-5.6-terra");
  await user.selectOptions(selects.reasoning, "high");

  rerender(sessionView(second));
  selects = composerSelects();
  expect(selects.approval).toHaveValue("on-request");
  expect(selects.model).toHaveValue("gpt-5.6-sol");
  expect(selects.reasoning).toHaveValue("");

  await user.selectOptions(selects.approval, "untrusted");
  await user.selectOptions(selects.model, "gpt-5.6-luna");
  await user.selectOptions(selects.reasoning, "low");

  rerender(sessionView(first));
  selects = composerSelects();
  expect(selects.approval).toHaveValue("never");
  expect(selects.model).toHaveValue("gpt-5.6-terra");
  expect(selects.reasoning).toHaveValue("high");

  unmount();
  render(sessionView(second));
  selects = composerSelects();
  expect(selects.approval).toHaveValue("untrusted");
  expect(selects.model).toHaveValue("gpt-5.6-luna");
  expect(selects.reasoning).toHaveValue("low");
});

test("reuses cached conversation events and restores each session scroll position", async () => {
  const first = thread("scroll-first");
  const second = thread("scroll-second");
  const { container, rerender } = render(sessionView(first));

  expect(await screen.findByText("No messages yet")).toBeInTheDocument();
  expect(fetch).toHaveBeenCalledTimes(1);
  let stream = container.querySelector<HTMLElement>(".event-stream")!;
  stream.scrollTop = 360;
  fireEvent.scroll(stream);

  rerender(sessionView(second));
  expect(await screen.findByText("No messages yet")).toBeInTheDocument();
  expect(fetch).toHaveBeenCalledTimes(2);
  stream = container.querySelector<HTMLElement>(".event-stream")!;
  stream.scrollTop = 120;
  fireEvent.scroll(stream);

  rerender(sessionView(first));
  expect(screen.getByText("No messages yet")).toBeInTheDocument();
  await waitFor(() => expect(container.querySelector<HTMLElement>(".event-stream")).toHaveProperty("scrollTop", 360));
  expect(fetch).toHaveBeenCalledTimes(2);

  rerender(sessionView(second));
  await waitFor(() => expect(container.querySelector<HTMLElement>(".event-stream")).toHaveProperty("scrollTop", 120));
  expect(fetch).toHaveBeenCalledTimes(2);

  rerender(sessionView(first, 1));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
});
