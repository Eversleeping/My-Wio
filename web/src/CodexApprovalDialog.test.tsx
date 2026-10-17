import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { CodexPage } from "./App";
import { I18nProvider } from "./i18n";
import type { Approval } from "./types";

const approval: Approval = {
  id: "approval-1",
  thread_id: "thread-1",
  request_id: "request-1",
  kind: "command",
  detail: { command: "go test ./..." },
  status: "pending",
  title: "Run tests",
  expires_at: "2026-07-23T12:00:00Z"
};

beforeEach(() => {
  window.localStorage.clear();
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn(async () => new Response("[]", { status: 200, headers: { "Content-Type": "application/json" } })));
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

function ApprovalPage({ pending = approval }: { pending?: Approval }) {
  const [approvals, setApprovals] = useState<Approval[]>([pending]);
  return <I18nProvider><CodexPage realtime={0} streamRevisions={{}} approvals={approvals} approvalSignal={0} reloadApprovals={() => setApprovals([])} notify={vi.fn()} selectedThreadID="" onSelectThread={vi.fn()} /></I18nProvider>;
}

test("closes the approval dialog after the final request is approved", async () => {
  const user = userEvent.setup();
  render(<ApprovalPage />);

  expect(await screen.findByRole("dialog", { name: "Pending approvals" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Approve once" }));

  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Pending approvals" })).not.toBeInTheDocument());
  expect(fetch).toHaveBeenCalledWith("/api/approvals/approval-1/decision", expect.objectContaining({ method: "POST" }));
});

test("renders interactive questions and sends selected and typed answers through the page", async () => {
  const pending: Approval = {
    ...approval,
    kind: "item/tool/requestUserInput",
    detail: { questions: [
      { id: "choice", header: "Choice", question: "Choose an option", options: [{ label: "A", description: "First option" }, { label: "B", description: "Second option" }] },
      { id: "details", header: "Details", question: "Add details", isOther: true }
    ] }
  };
  const user = userEvent.setup();
  render(<ApprovalPage pending={pending} />);

  expect(await screen.findByRole("radio", { name: "B Second option" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Approve once" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Submit answers" })).toBeDisabled();
  await user.click(screen.getByRole("radio", { name: "B Second option" }));
  await user.type(screen.getByRole("textbox", { name: "Your answer" }), "My custom details");
  await user.click(screen.getByRole("button", { name: "Submit answers" }));

  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Pending approvals" })).not.toBeInTheDocument());
  expect(fetch).toHaveBeenCalledWith("/api/approvals/approval-1/decision", expect.objectContaining({
    method: "POST",
    body: JSON.stringify({ decision: "answered", answers: { choice: { answers: ["B"] }, details: { answers: ["My custom details"] } } })
  }));
});
