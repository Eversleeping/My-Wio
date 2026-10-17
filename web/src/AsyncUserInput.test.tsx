import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { SessionView, clearCodexSessionMemory } from "./App";
import { I18nProvider } from "./i18n";
import type { StreamEvent, Thread } from "./types";

const callID = "call_0f9662ddc3cb40a697cd9d83e2e0d372";
const title = "你最喜欢哪个季节？可以选择一个选项，也可以在输入框里填写自己的答案。";
// Shape captured from the affected production conversation, including the
// fallback text that previously hid the structured questions.
const questionEvent: StreamEvent = {
  event_id: "question", stream_id: "async-test", sequence: 1, kind: "codex.item.completed", occurred_at: "2026-10-17T05:59:45Z",
  payload: { item: { delivery: "async", id: callID, phase: "final_answer", questions: [{ options: ["春天", "夏天", "秋天", "冬天"], title }], text: `${title}\n- 春天\n- 夏天\n- 秋天\n- 冬天`, type: "agentMessage" } }
};
const thread: Thread = { id: "async-test", workspace_id: "workspace", project_id: "project", codex_thread_id: "native", title: "Test", status: "running", path: "/srv/test", server_id: "server", server_name: "Server", project_name: "Project", created_at: "", updated_at: "", pinned_at: null, archived_at: null, project_pinned_at: null, project_hidden_at: null };
let history: StreamEvent[];
let current: Thread;
let requests: { path: string; body: { prompt: string } }[];
let rejectSteer: boolean;
let rejectSubmit: boolean;

beforeEach(() => {
  clearCodexSessionMemory();
  window.localStorage.clear();
  window.localStorage.setItem("wio_language", "en");
  history = [questionEvent]; current = { ...thread }; requests = []; rejectSteer = false; rejectSubmit = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path.endsWith("/refresh")) return new Response("{}", { status: 202 });
    if (init?.method === "POST") {
      const body = JSON.parse(String(init.body));
      requests.push({ path, body });
      if (rejectSubmit) return new Response(JSON.stringify({ error: "Try again" }), { status: 500 });
      if (rejectSteer && path.endsWith("/steer")) return new Response(JSON.stringify({ code: "steer_inactive", error: "Turn ended" }), { status: 409 });
      history = [...history, { event_id: "reply", stream_id: thread.id, sequence: 2, kind: "user.message", occurred_at: "2026-10-17T06:00:00Z", payload: { text: body.prompt } }];
      return new Response("{}", { status: 202 });
    }
    const body = path.includes("/events") ? history : path === `/api/threads/${thread.id}` ? current : path.endsWith("/codex/models") ? { supported: true, status: "succeeded", data: [] } : [];
    return new Response(JSON.stringify(body), { status: 200 });
  }));
});
afterEach(() => { vi.unstubAllGlobals(); clearCodexSessionMemory(); window.localStorage.clear(); });

function view(value = current) {
  return <I18nProvider><SessionView thread={value} approvals={[]} realtime={0} reloadApprovals={vi.fn()} notify={vi.fn()} onOpenFile={vi.fn()} onNewTask={vi.fn()} /></I18nProvider>;
}

test("renders the actual async message, selects an answer, and persists its reply across reload", async () => {
  const user = userEvent.setup();
  const { unmount } = render(view());
  const option = await screen.findByRole("radio", { name: "秋天" });
  expect(screen.getByRole("textbox", { name: "Your answer" })).toBeInTheDocument();
  await user.click(option);
  expect(option).toBeChecked();
  await user.click(screen.getByRole("button", { name: "Submit answers" }));
  await waitFor(() => expect(requests).toHaveLength(1));
  const reply = [{ questionItemId: JSON.stringify(["request_user_input_async", callID, 0]), question: title, answer: "秋天" }];
  expect(requests[0]).toEqual({ path: `/api/threads/${thread.id}/steer`, body: { prompt: `<send_user_message_question_reply>\n${JSON.stringify(reply)}\n</send_user_message_question_reply>`, images: [] } });
  expect(await screen.findByRole("button", { name: "Answers sent" })).toBeDisabled();
  unmount(); clearCodexSessionMemory(); render(view());
  expect(await screen.findByRole("radio", { name: "秋天" })).toBeChecked();
  expect(screen.getByRole("button", { name: "Answers sent" })).toBeDisabled();
  expect(screen.queryByText(/<send_user_message_question_reply>/)).not.toBeInTheDocument();
});

test("retains a custom answer after a failure and starts a new turn when the session is idle", async () => {
  current.status = "idle"; rejectSubmit = true;
  const user = userEvent.setup(); render(view());
  const input = await screen.findByRole("textbox", { name: "Your answer" });
  await user.click(screen.getByRole("radio", { name: "春天" }));
  await user.clear(input); await user.type(input, "雨季");
  expect(screen.getByRole("radio", { name: "春天" })).not.toBeChecked();
  await user.click(screen.getByRole("button", { name: "Submit answers" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Try again");
  expect(input).toHaveValue("雨季");
  rejectSubmit = false;
  await user.click(screen.getByRole("button", { name: "Submit answers" }));
  expect(await screen.findByRole("button", { name: "Answers sent" })).toBeDisabled();
  expect(requests[1].path).toBe(`/api/threads/${thread.id}/turns`);
  expect(requests[1].body.prompt).toContain('"answer":"雨季"');
});

test("handles a turn ending between status lookup and submitting the answer", async () => {
  rejectSteer = true;
  const user = userEvent.setup(); render(view());
  await user.click(await screen.findByRole("radio", { name: "夏天" }));
  await user.click(screen.getByRole("button", { name: "Submit answers" }));
  expect(await screen.findByRole("button", { name: "Answers sent" })).toBeDisabled();
  expect(requests.map(request => request.path)).toEqual([`/api/threads/${thread.id}/steer`, `/api/threads/${thread.id}/turns`]);
});

test("keeps archived async questions read only", async () => {
  current.archived_at = "2026-10-17T07:00:00Z";
  render(view());
  expect(await screen.findByRole("radio", { name: "春天" })).toBeDisabled();
  expect(screen.getByRole("textbox", { name: "Your answer" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Submit answers" })).toBeDisabled();
});
