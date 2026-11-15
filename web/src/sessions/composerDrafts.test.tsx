import { act, renderHook } from "@testing-library/react";
import { expect, test } from "vitest";
import { clearComposerDrafts, useComposerDraft } from "./composerDrafts";
import { clearSessionMemory } from "./memory";
import type { Engine } from "./types";

test("restores text, attachments and editing context across unmounts and isolates equal IDs", () => {
  const first = renderHook(() => useComposerDraft("codex", "same"));
  act(() => {
    first.result.current.setPrompt("Keep this edit");
    first.result.current.setImages([{ id: "image", dataURL: "data:image/png;base64,AAAA" }]);
    first.result.current.setEditingEventID("event");
    first.result.current.setComposerMode("goal");
  });
  first.unmount();
  const second = renderHook(({ engine, id }) => useComposerDraft(engine, id), { initialProps: { engine: "claude" as Engine, id: "same" } });
  expect(second.result.current.prompt).toBe("");
  act(() => second.result.current.setPrompt("Claude draft"));
  second.rerender({ engine: "codex", id: "other" });
  expect(second.result.current.prompt).toBe("");
  second.rerender({ engine: "codex", id: "same" });
  expect(second.result.current).toMatchObject({ prompt: "Keep this edit", images: [{ id: "image" }], editingEventID: "event", composerMode: "goal" });
  act(() => clearComposerDrafts({ engine: "codex", id: "same" }));
  expect(second.result.current).toMatchObject({ prompt: "", images: [], editingEventID: "", composerMode: "message" });
  second.rerender({ engine: "claude", id: "same" });
  expect(second.result.current.prompt).toBe("Claude draft");
  act(() => clearSessionMemory());
  expect(second.result.current.prompt).toBe("");
});

test("successful send setters clear the saved draft and support functional attachment updates", () => {
  const view = renderHook(() => useComposerDraft("gemini", "sent"));
  act(() => {
    view.result.current.setPrompt("Send this");
    view.result.current.setImages(images => [...images, { id: "image", dataURL: "image" }]);
    view.result.current.setEditingEventID("edit");
  });
  act(() => {
    view.result.current.setPrompt("");
    view.result.current.setImages([]);
    view.result.current.setEditingEventID("");
  });
  view.unmount();
  const restored = renderHook(() => useComposerDraft("gemini", "sent"));
  expect(restored.result.current).toMatchObject({ prompt: "", images: [], editingEventID: "" });
});
