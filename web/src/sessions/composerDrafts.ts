import { useMemo, useSyncExternalStore, type SetStateAction } from "react";
import { sessionKey, type Engine, type SessionRef } from "./types";

type Draft = {
  prompt: string;
  images: { id: string; dataURL: string }[];
  editingEventID: string;
  composerMode: "message" | "goal";
};
const empty: Draft = { prompt: "", images: [], editingEventID: "", composerMode: "message" };
// Drafts live only in this tab's memory; never write prompts or images to browser storage.
const drafts = new Map<string, Draft>();
const listeners = new Set<() => void>();
const emit = () => listeners.forEach(listener => listener());
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };

export function clearComposerDrafts(ref?: SessionRef) {
  if (ref) drafts.delete(sessionKey(ref));
  else drafts.clear();
  emit();
}

export function useComposerDraft(engine: Engine, id: string) {
  const key = sessionKey({ engine, id });
  const draft = useSyncExternalStore(subscribe, () => drafts.get(key) ?? empty);
  const setters = useMemo(() => {
    function setter<K extends keyof Draft>(field: K) {
      return (value: SetStateAction<Draft[K]>) => {
        const current = drafts.get(key) ?? empty;
        const next = { ...current, [field]: typeof value === "function" ? (value as (previous: Draft[K]) => Draft[K])(current[field]) : value };
        if (!next.prompt && !next.images.length && !next.editingEventID && next.composerMode === "message") drafts.delete(key);
        else drafts.set(key, next);
        emit();
      };
    }
    return { setPrompt: setter("prompt"), setImages: setter("images"), setEditingEventID: setter("editingEventID"), setComposerMode: setter("composerMode") };
  }, [key]);
  return { ...draft, ...setters };
}
