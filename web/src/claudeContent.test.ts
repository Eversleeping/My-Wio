import { expect, test } from "vitest";
import { claudeDisplayEvents, claudeToolPresentation } from "./claudeContent";
import type { StreamEvent } from "./types";

const event = (kind: string, payload: Record<string, unknown>, sequence: number): StreamEvent => ({ event_id: `e${sequence}`, stream_id: "claude-1", kind: `claude.${kind}`, sequence, occurred_at: "2026-11-05T00:00:00Z", payload });

test("pairs compaction progress with the matching operation and separates replies at the boundary", () => {
  const items = claudeDisplayEvents([
    event("message", { text: "Before" }, 1),
    event("compaction.started", { operation_id: "one" }, 2),
    event("compaction.started", { operation_id: "one" }, 3),
    event("compaction.started", { operation_id: "two" }, 4),
    event("compaction.completed", { operation_id: "one", trigger: "auto", pre_tokens: 180000, post_tokens: 24000 }, 5),
    event("message", { text: "After" }, 6),
    event("turn.completed", { operation_id: "two", status: "interrupted" }, 7),
    event("compaction.completed", { operation_id: "one", trigger: "manual", pre_tokens: 0 }, 8),
  ], "Denied");
  expect(items.map(item => item.role)).toEqual(["assistant", "compaction", "compaction", "assistant", "compaction"]);
  expect(items[1].compaction).toEqual({ status: "completed", trigger: "auto", preTokens: 180000, postTokens: 24000 });
  expect(items[2].compaction?.status).toBe("interrupted");
  expect(items[4].compaction).toEqual({ status: "completed", trigger: "manual", preTokens: 0, postTokens: undefined });
});

test.each(["failed", "idle"])("does not report compaction success without a native boundary (%s)", status => {
  const items = claudeDisplayEvents([event("compaction.started", { operation_id: "one" }, 1), event("turn.completed", { operation_id: "other", status: "idle" }, 2), event("turn.completed", { operation_id: "one", status }, 3), event("compaction.started", { operation_id: "one" }, 4), event("compaction.failed", { operation_id: "one" }, 5)], "Denied");
  expect(items[0].compaction?.status).toBe(status === "failed" ? "failed" : "unknown");
  expect(items[1].compaction?.status).toBe("failed");
  const invalid = claudeDisplayEvents([event("compaction.completed", { trigger: "future", pre_tokens: -1, post_tokens: "123" }, 1)], "Denied")[0];
  expect(invalid.compaction).toEqual({ status: "completed", trigger: undefined, preTokens: undefined, postTokens: undefined });
});

test("retains the first timestamp while streaming and starts a new one after a tool", () => {
  const first = event("message", { text: "Checking" }, 2);
  const later = { ...event("message", { text: " now" }, 3), occurred_at: "2026-11-05T00:00:10Z" };
  const next = { ...event("message", { text: "Done" }, 5), occurred_at: "2026-11-05T00:01:00Z" };
  const before = claudeDisplayEvents([event("user.message", { text: "Hi" }, 1), first], "Denied");
  const after = claudeDisplayEvents([event("user.message", { text: "Hi" }, 1), first, later, event("tool_use", { id: "tool", name: "Read" }, 4), next], "Denied");
  expect(after[1]).toMatchObject({ text: "Checking now", occurredAt: before[1].occurredAt, endEventID: "e3" });
  expect(after[0].occurredAt).toBe(first.occurred_at);
  expect(after[3].occurredAt).toBe(next.occurred_at);
});

test.each([undefined, "", "invalid"])("does not invent a timestamp for incomplete history (%s)", occurred_at => {
  const item = { ...event("message", { text: "Old reply" }, 1), occurred_at } as StreamEvent;
  expect(claudeDisplayEvents([item], "Denied")[0].occurredAt).toBeUndefined();
});

test("groups consecutive commands and pairs results without counting them twice", () => {
  const items = claudeDisplayEvents([
    event("message", { text: "Checking" }, 1),
    event("tool_use", { id: "one", name: "Bash", input: { command: "git status" } }, 2),
    event("tool_use", { id: "two", name: "Bash", input: { command: "git status" } }, 3),
    event("tool_result", { id: "two", content: "second output", is_error: true }, 4),
    event("tool_result", { id: "one", content: "first output" }, 5),
    event("message", { text: "All done" }, 6),
  ], "Denied");
  expect(items.map(item => item.role)).toEqual(["assistant", "toolGroup", "assistant"]);
  expect(items[1].tools).toHaveLength(2);
  expect(items[1].tools?.[0]).toMatchObject({ name: "Bash", output: "first output", failed: false });
  expect(items[1].tools?.[1]).toMatchObject({ output: "second output", failed: true });
});

test.each(["message", "user.message", "notice"])("does not group across a %s boundary", kind => {
  const items = claudeDisplayEvents([
    event("tool_use", { id: "same", name: "Bash" }, 1),
    event("tool_result", { id: "same", content: "old" }, 2),
    event(kind, { text: "Boundary" }, 3),
    event("tool_use", { id: "same", name: "Read" }, 4),
    event("tool_result", { id: "same", content: "new" }, 5),
  ], "Denied");
  expect(items).toHaveLength(3);
  expect(items[0].tools?.[0].output).toBe("old");
  expect(items[2].tools?.[0].output).toBe("new");
});

test("keeps different operations separate and retains output-only history", () => {
  const items = claudeDisplayEvents([
    event("tool_result", { id: "same", operation_id: "one", content: "older output" }, 1),
    event("tool_use", { id: "same", operation_id: "two", name: "Read" }, 2),
    event("tool_result", { id: "same", operation_id: "two", content: "new output" }, 3),
  ], "Denied");
  expect(items).toHaveLength(2);
  expect(items[0].tools?.[0].output).toBe("older output");
  expect(items[1].tools?.[0]).toMatchObject({ name: "Read", output: "new output" });
});

test("formats native Bash commands and text blocks without JSON escaping or losing options", () => {
  const command = 'npx prettier --check README.md 2>&1 | grep -v "npm notice" | tail -2';
  const display = claudeToolPresentation({ id: "bash", name: "Bash", input: JSON.stringify({ command, description: "Recheck README formatting", timeout: 120000, run_in_background: false }), output: JSON.stringify([{ type: "text", text: "Checking formatting…" }, { type: "text", text: "All files match." }]) });
  expect(display).toMatchObject({ title: command, kind: "shell", input: `${command}\n(Recheck README formatting)\ntimeout: 120000\nrun_in_background: false`, output: "Checking formatting…\nAll files match." });
});

test("summarizes file and search tools and preserves unfamiliar parameters and mixed output", () => {
  const read = claudeToolPresentation({ id: "read", name: "Read", input: JSON.stringify({ file_path: "/project/README.md", offset: 0, limit: 50 }) });
  expect(read).toMatchObject({ title: "Read: /project/README.md", kind: "read", input: "file_path: /project/README.md\noffset: 0\nlimit: 50" });
  expect(claudeToolPresentation({ id: "grep", name: "Grep", input: JSON.stringify({ pattern: "TODO", path: "/project" }) })).toMatchObject({ title: "Grep: TODO", kind: "search" });
  const input = JSON.stringify({ options: { safe: true }, text: "first\nsecond", old_string: "", values: [1, 2] });
  const output = JSON.stringify([{ type: "text", text: "Screenshot" }, { type: "image", data: "retained" }]);
  const unknown = claudeToolPresentation({ id: "mcp", name: "mcp__custom", input, output });
  expect(unknown.input).toContain('options: {\n  "safe": true\n}');
  expect(unknown.input).toContain("text: first\nsecond\nold_string: \nvalues: [");
  expect(unknown.output).toBe(output);
  expect(claudeToolPresentation({ id: "old", name: "Legacy", input: "unstructured", output: "plain output" })).toMatchObject({ input: "unstructured", output: "plain output" });
});

test("keeps start times and result states and closes pending tools only for the matching operation", () => {
  const items = claudeDisplayEvents([
    event("tool_use", { id: "one", operation_id: "one", name: "Bash" }, 1),
    { ...event("tool_result", { id: "one", operation_id: "one", content: "", is_error: false }, 2), occurred_at: "2026-11-05T00:00:10Z" },
    event("tool_use", { id: "two", operation_id: "one", name: "Bash" }, 3),
    event("tool_use", { id: "three", operation_id: "two", name: "Read" }, 4),
    event("turn.completed", { operation_id: "one", status: "interrupted" }, 5),
    event("tool_result", { id: "three", operation_id: "two", content: "error", is_error: true }, 6),
  ], "Denied");
  expect(items[0].tools?.[0]).toMatchObject({ occurredAt: "2026-11-05T00:00:00Z", output: "", status: "completed" });
  expect(items[0].tools?.[1].status).toBe("interrupted");
  expect(items[1].tools?.[0].status).toBe("failed");
});
