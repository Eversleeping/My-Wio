import { expect, test } from "vitest";
import { geminiMessages, groupGeminiMessages, type GeminiMessage } from "./geminiContent";
import { locationFor, realtimeScopesForEvent } from "./App";
import type { StreamEvent } from "./types";

function event(sequence: number, kind: string, payload: unknown): StreamEvent { return { event_id: String(sequence), stream_id: "session", sequence, kind, payload, occurred_at: "2026-10-24T00:00:00Z" }; }

function replayFixture(): StreamEvent[] {
  const output = [{ type: "content", content: { type: "text", text: "file contents" } }];
  return [
    event(1, "gemini.init", { operation_id: "old", session_id: "native" }),
    event(2, "gemini.tool_use", { operation_id: "old", tool_id: "read_file__call_1", tool_name: "README.md", parameters: [] }),
    event(3, "gemini.tool_result", { operation_id: "old", tool_id: "read_file__call_1", status: "completed", output }),
    event(4, "gemini.turn.completed", { operation_id: "old", status: "interrupted" }),
    event(5, "gemini.user.message", { operation_id: "new", text: "Continue" }),
    event(6, "gemini.init", { operation_id: "new", session_id: "native" }),
    event(7, "gemini.tool_use", { operation_id: "new", tool_id: "read_file__call_1", tool_name: "ReadFile", parameters: output }),
  ];
}

test("hides legacy ACP history snapshots without changing original calls or timestamps", () => {
  const events = replayFixture();
  const before = JSON.stringify(events);
  const messages = geminiMessages([...events,
    event(8, "gemini.tool_use", { operation_id: "new", tool_id: "read_file__call_2", tool_name: "README.md", parameters: [] }),
    event(9, "gemini.tool_result", { operation_id: "new", tool_id: "read_file__call_2", status: "completed", output: "new contents" }),
    event(10, "gemini.result", { operation_id: "new", status: "success", stats: { tool_calls: 2, input_tokens: 30 } }),
  ]);
  expect(messages.filter(m => m.role === "tool")).toMatchObject([
    { id: "2", time: events[1].occurred_at, toolStatus: "completed", toolOutput: "file contents" },
    { id: "8", toolStatus: "completed", toolOutput: "new contents" },
  ]);
  expect(JSON.stringify(events)).toBe(before);
  expect(messages.at(-1)?.stats).toMatchObject({ tool_calls: 1, input_tokens: 30 });
});

test.each(["new result", "different session", "different content", "real title", "explicit status", "after live call", "missing previous result"])("preserves possible real executions: %s", scenario => {
  let events = replayFixture();
  const replay = events[6].payload as Record<string, unknown>;
  if (scenario === "new result") events.push(event(8, "gemini.tool_result", { operation_id: "new", tool_id: "read_file__call_1", status: "failed", output: "new error" }));
  if (scenario === "different session") events[5].payload = { operation_id: "new", session_id: "other" };
  if (scenario === "different content") replay.parameters = [];
  if (scenario === "real title") replay.tool_name = "README.md";
  if (scenario === "explicit status") replay.status = "pending";
  if (scenario === "after live call") events.splice(6, 0, event(20, "gemini.tool_use", { operation_id: "new", tool_id: "other", parameters: [] }));
  if (scenario === "missing previous result") events = events.filter(e => e.sequence !== 3);
  expect(geminiMessages(events).some(m => m.id === "7")).toBe(true);
});

test("respects terminal status on a full tool snapshot", () => {
  expect(geminiMessages([event(1, "gemini.tool_use", { tool_id: "t", status: "failed" })])[0].toolStatus).toBe("failed");
});

test("groups tool calls between replies once, including running calls and their original timestamps", () => {
  const events = [
    event(1, "gemini.message", { role: "assistant", content: "Starting", operation_id: "op1" }),
    event(2, "gemini.tool_use", { tool_id: "read_file__1", operation_id: "op1" }),
    { ...event(3, "gemini.tool_use", { tool_id: "run_shell_command__2", operation_id: "op1" }), occurred_at: "2026-10-24T00:00:05Z" },
  ];
  const running = groupGeminiMessages(geminiMessages(events));
  expect(running[1]).toMatchObject({ role: "commandGroup", id: "2", items: [{ id: "2", toolStatus: "running" }, { id: "3", toolStatus: "running" }] });
  const messages = geminiMessages([...events,
    event(4, "gemini.message", { role: "assistant", content: "Finished", operation_id: "op1" }),
    event(5, "gemini.tool_result", { tool_id: "run_shell_command__2", status: "failed", output: "error", operation_id: "op1" }),
    event(6, "gemini.tool_result", { tool_id: "read_file__1", status: "completed", output: "contents", operation_id: "op1" }),
    event(7, "gemini.tool_use", { tool_id: "run_shell_command__3", operation_id: "op1" }),
  ]);
  const grouped = groupGeminiMessages(messages);
  expect(grouped.map(item => item.role)).toEqual(["assistant", "commandGroup", "assistant", "commandGroup"]);
  expect(grouped[1]).toMatchObject({ id: "2", items: [
    { id: "2", time: "2026-10-24T00:00:00Z", toolStatus: "completed" },
    { id: "3", time: "2026-10-24T00:00:05Z", toolStatus: "failed" },
  ] });
  expect(messages).toHaveLength(5);
});

test.each(["assistant", "user", "question", "error", "status"] as const)("does not group commands across a %s message", role => {
  const tool: GeminiMessage = { id: "1", role: "tool", text: "", time: "2026-10-24T00:00:00Z", operationID: "op1" };
  const items = groupGeminiMessages([tool, { ...tool, id: "2", role }, { ...tool, id: "3" }]);
  expect(items.map(item => item.role)).toEqual(["commandGroup", role, "commandGroup"]);
});

test("does not group adjacent tools from different turns", () => {
  const items = groupGeminiMessages(geminiMessages([
    event(1, "gemini.tool_use", { tool_id: "same", operation_id: "op1" }),
    event(2, "gemini.tool_use", { tool_id: "same", operation_id: "op2" }),
  ]));
  expect(items).toMatchObject([{ role: "commandGroup", items: [{ id: "1" }] }, { role: "commandGroup", items: [{ id: "2" }] }]);
});

test("merges assistant deltas but preserves tool boundaries and later turns", () => {
  const messages = geminiMessages([
    event(1, "gemini.user.message", { text: "first", operation_id: "op1" }),
    event(2, "gemini.message", { role: "assistant", content: "Hello", delta: true, operation_id: "op1" }),
    event(3, "gemini.message", { role: "assistant", content: " world", delta: true, operation_id: "op1" }),
    event(4, "gemini.tool_use", { tool_name: "read_file", parameters: { path: "README.md" }, operation_id: "op1" }),
    event(5, "gemini.message", { role: "assistant", content: "After tool", delta: true, operation_id: "op1" }),
    event(6, "gemini.message", { role: "assistant", content: "Next turn", delta: true, operation_id: "op2" }),
    event(7, "gemini.result", { error: { message: "quota exhausted" }, operation_id: "op2" })
  ]);
  expect(messages.map(item => item.text)).toEqual(["first", "Hello world", '{\n  "path": "README.md"\n}', "After tool", "Next turn", "quota exhausted"]);
});

test("routes Gemini links and realtime events independently of Codex", () => {
  expect(locationFor("gemini", "session-1")).toContain("view=gemini&thread=session-1");
  expect(realtimeScopesForEvent({ kind: "gemini.message" })).toEqual(["gemini", "operations"]);
});

test("pairs ACP tool calls and results by operation and ID while retaining input and plain output", () => {
  const block = (text: string) => [{ type: "content", content: { type: "text", text } }];
  const messages = geminiMessages([
    event(1, "gemini.tool_use", { tool_id: "read_file__call_1", tool_name: "README.md", parameters: block("Read file"), operation_id: "op1" }),
    event(2, "gemini.tool_use", { tool_id: "read_file__call_2", tool_name: "README.md", parameters: [], operation_id: "op1" }),
    event(3, "gemini.tool_result", { tool_id: "read_file__call_2", output: block("second file"), status: "completed", operation_id: "op1" }),
    event(4, "gemini.message", { role: "assistant", content: "Working", operation_id: "op1" }),
    event(5, "gemini.tool_result", { tool_id: "read_file__call_1", output: block("first file"), status: "completed", operation_id: "op1" }),
    event(6, "gemini.tool_use", { tool_id: "read_file__call_1", tool_name: "README.md", parameters: {}, operation_id: "op2" }),
    event(7, "gemini.tool_result", { tool_id: "read_file__call_1", error: "Permission denied", status: "error", operation_id: "op2" }),
  ]);
  expect(messages).toHaveLength(4);
  expect(messages[0]).toMatchObject({ id: "1", title: "README.md", toolInput: "Read file", toolOutput: "first file", toolStatus: "completed" });
  expect(messages[1]).toMatchObject({ id: "2", toolOutput: "second file" });
  expect(messages[3]).toMatchObject({ id: "6", toolOutput: "Permission denied", toolStatus: "error" });
});

test("keeps legacy calls, orphan results and missing-ID records visible", () => {
  const messages = geminiMessages([
    event(1, "gemini.tool_use", { tool_id: "t1", tool_name: "read_file", parameters: { path: "README.md" } }),
    event(2, "gemini.tool_result", { tool_id: "t1", status: "success", output: "contents" }),
    event(3, "gemini.tool_result", { tool_id: "orphan", status: "failed", output: "missing file" }),
    event(4, "gemini.tool_use", { tool_name: "no ID" }),
    event(5, "gemini.tool_result", { tool_name: "no ID", status: "success", output: "done" }),
  ]);
  expect(messages).toHaveLength(4);
  expect(messages[0]).toMatchObject({ title: "read_file", toolName: "read_file", toolInput: '{\n  "path": "README.md"\n}', toolOutput: "contents", toolStatus: "success" });
  expect(messages[1]).toMatchObject({ toolOutput: "missing file", toolStatus: "failed" });
});

test("uses numeric CLI usage and safely handles already redacted history", () => {
  const messages = geminiMessages([
    event(1, "gemini.result", { stats: { input_tokens: 12, output_tokens: 0, tool_calls: 2 } }),
    event(2, "gemini.result", { stats: { input_tokens: "[REDACTED]", input: 54, output_tokens: "[REDACTED]", tool_calls: 0 } }),
    event(3, "gemini.result", { stats: { input_tokens: "secret", output_tokens: -2, output: 7, tool_calls: "secret" } })
  ]);
  expect(messages.map(item => item.stats)).toEqual([
    { input_tokens: 12, output_tokens: 0, tool_calls: 2 },
    { input_tokens: 54, output_tokens: undefined, tool_calls: 0 },
    { input_tokens: undefined, output_tokens: 7, tool_calls: undefined }
  ]);
});


test("reconstructs question answers and image attachments after refresh", () => {
  const image={data_url:"data:image/png;base64,eA=="};
  const messages=geminiMessages([
    event(1,"gemini.user.message",{text:"",images:[image,{data_url:"javascript:alert(1)"}],operation_id:"op"}),
    event(2,"gemini.question",{questions:[{id:"q",question:"Which?"}],operation_id:"op"}),
    event(3,"gemini.question.answered",{request_id:"2",answers:{q:{answers:["Blue"]}},operation_id:"op"}),
    event(4,"gemini.message",{role:"assistant",content:"Continued",delta:true,operation_id:"op"})
  ]);
  expect(messages[0].images).toEqual([image]);
  expect(messages[1]).toMatchObject({role:"question",operationID:"op",answers:{q:"Blue"}});
  expect(messages[2].text).toBe("Continued");
});
