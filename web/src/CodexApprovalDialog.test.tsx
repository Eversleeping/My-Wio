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

function ApprovalPage() {
  const [approvals, setApprovals] = useState<Approval[]>([approval]);
  return <I18nProvider><CodexPage realtime={0} streamRevisions={{}} approvals={approvals} approvalSignal={0} reloadApprovals={() => setApprovals([])} notify={vi.fn()} selectedThreadID="" onSelectThread={vi.fn()} /></I18nProvider>;
}

test("closes the approval dialog after the final request is approved", async () => {
  const user = userEvent.setup();
  render(<ApprovalPage />);

  expect(screen.getByRole("dialog", { name: "Pending approvals" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Approve once" }));

  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Pending approvals" })).not.toBeInTheDocument());
  expect(fetch).toHaveBeenCalledWith("/api/approvals/approval-1/decision", expect.objectContaining({ method: "POST" }));
});
