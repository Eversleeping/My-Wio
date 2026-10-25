import { expect, test } from "vitest";
import { geminiMessages } from "./geminiContent";
import { locationFor, realtimeScopesForEvent } from "./App";
import type { StreamEvent } from "./types";

function event(sequence: number, kind: string, payload: unknown): StreamEvent { return { event_id: String(sequence), stream_id: "session", sequence, kind, payload, occurred_at: "2026-10-24T00:00:00Z" }; }

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
