import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { CollaborationTaskDialog, collaborationTaskPrompt, useSubmitPrompt } from "./CollaborationTaskDialog";

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

const grant = (extra: object = {}) => ({ supported: true, grant: { enabled: true, worktree: true, target_modes: { claude: "default" }, ...extra }, links: [], requests: [] });

function stubAPI(state: object) {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url.includes("/collaboration")) return new Response(JSON.stringify(state));
    if (url.endsWith("/claude/models")) return new Response(JSON.stringify({ configured: true, models: [{ id: "claude-opus-5-5", name: "Claude Opus 5.5" }] }));
    if (url.endsWith("/codex/models/refresh")) return new Response("{}");
    if (url.endsWith("/codex/models")) return new Response(JSON.stringify({ status: "succeeded", supported: true, data: [{ id: "m", model: "gpt-5.6-sol", displayName: "GPT-5.6 Sol", supportedReasoningEfforts: [{ reasoningEffort: "low", description: "" }, { reasoningEffort: "high", description: "" }] }] }));
    if (url.endsWith("/gemini/models")) return new Response(JSON.stringify({ configured: true, models: [{ id: "gemini-3-flash", name: "Gemini 3 Flash" }] }));
    return new Response("[]");
  }));
  return calls;
}

test("builds exact create_session arguments from fetched models and efforts", async () => {
  localStorage.setItem("wio_language", "en");
  const calls = stubAPI(grant());
  const submitted: string[] = [];
  const user = userEvent.setup();
  render(<I18nProvider><CollaborationTaskDialog open engine="codex" sessionID="source" workspaceID="workspace" currentModel="gpt-5.6-sol" onClose={() => undefined} onSubmit={value => submitted.push(value)} /></I18nProvider>);
  expect(await screen.findByRole("option", { name: "GPT-5.6 Sol · gpt-5.6-sol" })).toBeInTheDocument();
  expect(screen.getByRole("option", { name: "Same as this session (gpt-5.6-sol)" })).toBeInTheDocument();
  await user.selectOptions(screen.getByRole("combobox", { name: "Agent" }), "claude");
  await user.selectOptions(await screen.findByRole("combobox", { name: "Model" }), await screen.findByRole("option", { name: "Claude Opus 5.5 · claude-opus-5-5" }));
  expect(screen.getByRole("option", { name: "Server default model" })).toBeInTheDocument();
  await user.selectOptions(screen.getByRole("combobox", { name: "Thinking level" }), "xhigh");
  await user.selectOptions(screen.getByRole("combobox", { name: "Working directory" }), "worktree");
  await user.type(screen.getByRole("textbox", { name: "Session title" }), "Review");
  await user.type(screen.getByRole("textbox", { name: "Task" }), "Review the parser");
  await user.click(screen.getByRole("button", { name: "Send to model" }));
  expect(submitted).toHaveLength(1);
  expect(submitted[0]).toContain('"engine": "claude",\n  "model": "claude-opus-5-5",\n  "effort": "xhigh",\n  "directory_mode": "worktree",\n  "title": "Review"');
  expect(submitted[0]).toContain("wio_create_session");
  expect(submitted[0].endsWith("Task:\nReview the parser")).toBe(true);
  expect(calls.some(url => url.endsWith("/workspaces/workspace/claude/models"))).toBe(true);
});

test("blocks targets the collaboration grant does not allow", async () => {
  localStorage.setItem("wio_language", "en");
  stubAPI(grant({ worktree: false, target_modes: {} }));
  const user = userEvent.setup();
  render(<I18nProvider><CollaborationTaskDialog open engine="claude" sessionID="source" workspaceID="workspace" onClose={() => undefined} onSubmit={() => undefined} /></I18nProvider>);
  await user.type(screen.getByRole("textbox", { name: "Task" }), "Do it");
  await waitFor(() => expect(screen.getByRole("button", { name: "Send to model" })).toBeEnabled());
  expect(screen.getByRole("option", { name: "Separate worktree (not granted)" })).toBeDisabled();
  await user.selectOptions(screen.getByRole("combobox", { name: "Agent" }), "gemini");
  expect(screen.getByRole("alert")).toHaveTextContent("Creating Gemini sessions is not granted");
  expect(screen.getByRole("button", { name: "Send to model" })).toBeDisabled();
});

test("requires collaboration to be enabled", async () => {
  localStorage.setItem("wio_language", "zh-CN");
  stubAPI(grant({ enabled: false }));
  render(<I18nProvider><CollaborationTaskDialog open engine="gemini" sessionID="source" workspaceID="workspace" onClose={() => undefined} onSubmit={() => undefined} /></I18nProvider>);
  expect(await screen.findByRole("alert")).toHaveTextContent("当前会话未开启会话协作");
  expect(screen.getByRole("button", { name: /交给模型创建/ })).toBeDisabled();
});

test("omits default model and effort and writes Chinese instructions", () => {
  const prompt = collaborationTaskPrompt({ engine: "codex", model: "", effort: "", directoryMode: "shared", title: " ", content: " 修复登录 " }, true);
  expect(prompt).toContain('{\n  "engine": "codex",\n  "directory_mode": "shared"\n}');
  expect(prompt).toContain("未列出的 model、effort 不要传");
  expect(prompt.endsWith("协作内容：\n修复登录")).toBe(true);
});

test("submits the composer once the generated prompt is rendered", async () => {
  const sent: string[] = [];
  function Composer() {
    const [prompt, setPrompt] = useState("");
    const form = useRef<HTMLFormElement>(null);
    const submit = useSubmitPrompt(form, prompt, setPrompt);
    return <form ref={form} onSubmit={event => { event.preventDefault(); sent.push(prompt); }}><textarea aria-label="prompt" value={prompt} readOnly /><button type="button" onClick={() => submit("generated")}>go</button></form>;
  }
  const user = userEvent.setup();
  render(<Composer />);
  await user.click(screen.getByRole("button", { name: "go" }));
  await waitFor(() => expect(sent).toEqual(["generated"]));
});
