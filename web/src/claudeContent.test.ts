import { expect, test } from "vitest";
import { claudeDisplayEvents } from "./claudeContent";
import type { StreamEvent } from "./types";

const event = (kind: string, payload: Record<string, unknown>, sequence: number): StreamEvent => ({ event_id: `e${sequence}`, stream_id: "claude-1", kind: `claude.${kind}`, sequence, occurred_at: "2026-11-05T00:00:00Z", payload });

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
