export interface CodexComposerPreferences {
  approvalMode: string;
  model: string;
  reasoningEffort: string;
}

export const defaultCodexComposerPreferences: CodexComposerPreferences = {
  approvalMode: "on-request",
  model: "gpt-5.6-sol",
  reasoningEffort: ""
};

const storagePrefix = "wio_codex_composer_preferences_v1:";
const approvalModes = new Set(["on-request", "untrusted", "never"]);
const reasoningEfforts = new Set(["", "low", "medium", "high", "xhigh", "max"]);

function storageKey(threadID: string) {
  return `${storagePrefix}${threadID}`;
}

function normalize(value: unknown): CodexComposerPreferences {
  if (!value || typeof value !== "object") return { ...defaultCodexComposerPreferences };
  const candidate = value as Partial<CodexComposerPreferences>;
  return {
    approvalMode: typeof candidate.approvalMode === "string" && approvalModes.has(candidate.approvalMode) ? candidate.approvalMode : defaultCodexComposerPreferences.approvalMode,
    model: typeof candidate.model === "string" && candidate.model.length <= 200 ? candidate.model : defaultCodexComposerPreferences.model,
    reasoningEffort: typeof candidate.reasoningEffort === "string" && reasoningEfforts.has(candidate.reasoningEffort) ? candidate.reasoningEffort : defaultCodexComposerPreferences.reasoningEffort
  };
}

export function loadCodexComposerPreferences(threadID: string): CodexComposerPreferences {
  if (!threadID || typeof window === "undefined") return { ...defaultCodexComposerPreferences };
  try {
    const stored = window.localStorage.getItem(storageKey(threadID));
    return stored ? normalize(JSON.parse(stored)) : { ...defaultCodexComposerPreferences };
  } catch {
    return { ...defaultCodexComposerPreferences };
  }
}

export function saveCodexComposerPreferences(threadID: string, preferences: CodexComposerPreferences) {
  if (!threadID || typeof window === "undefined") return;
  try {
    window.localStorage.setItem(storageKey(threadID), JSON.stringify(normalize(preferences)));
  } catch {
    // The in-memory selection still works when browser storage is unavailable.
  }
}

export function clearCodexComposerPreferences(threadID: string) {
  if (!threadID || typeof window === "undefined") return;
  try {
    window.localStorage.removeItem(storageKey(threadID));
  } catch {
    // Ignore unavailable browser storage while deleting the server-side session.
  }
}
