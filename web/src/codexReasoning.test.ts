import { expect, test } from "vitest";
import { compatibleReasoningEffort, reasoningOptionsFor, type CodexModel } from "./codexModels";
import { loadCodexComposerPreferences, saveCodexComposerPreferences } from "./codexComposerPreferences";

const models: CodexModel[] = [
  { id: "deep", model: "deep", displayName: "Deep", description: "", isDefault: true, defaultReasoningEffort: "high", supportedReasoningEfforts: [{ reasoningEffort: "high", description: "High" }, { reasoningEffort: "ultra", description: "Ultra" }] },
  { id: "fast", model: "fast", displayName: "Fast", description: "", isDefault: false, defaultReasoningEffort: "low", supportedReasoningEfforts: [{ reasoningEffort: "low", description: "Low" }] },
  { id: "fixed", model: "fixed", displayName: "Fixed", description: "", isDefault: false, supportedReasoningEfforts: [] }
];

test("reasoning choices follow each model including ultra and no-effort models", () => {
  expect(reasoningOptionsFor(models, "deep").map(item => item.value)).toEqual(["high", "ultra"]);
  expect(reasoningOptionsFor(models, "fast").map(item => item.value)).toEqual(["low"]);
  expect(reasoningOptionsFor(models, "fixed")).toEqual([]);
  expect(reasoningOptionsFor(models, "custom").some(item => item.value === "ultra")).toBe(true);
});

test("switching models resets incompatible effort but preserves unknown server defaults", () => {
  expect(compatibleReasoningEffort(models, "fast", "ultra")).toBe("low");
  expect(compatibleReasoningEffort(models, "deep", "ultra")).toBe("ultra");
  expect(compatibleReasoningEffort(models, "fixed", "high")).toBe("");
  expect(compatibleReasoningEffort(models, "custom", "ultra")).toBe("ultra");
  expect(compatibleReasoningEffort(models, "", "ultra")).toBe("ultra");
  expect(compatibleReasoningEffort(models, "deep", "")).toBe("");
});

test("ultra survives session preferences without crossing sessions", () => {
  saveCodexComposerPreferences("reasoning-test", { approvalMode: "on-request", model: "deep", reasoningEffort: "ultra" });
  expect(loadCodexComposerPreferences("reasoning-test").reasoningEffort).toBe("ultra");
  expect(loadCodexComposerPreferences("other-reasoning-test").reasoningEffort).toBe("");
  window.localStorage.clear();
});
