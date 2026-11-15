import { clearComposerDrafts } from "./composerDrafts";
import { clearFileDrafts } from "../workspaceFileEditing";
import type { Engine } from "./types";

type SessionMemory = { clear: (id?: string) => void; invalidate: (id: string, sequence: number | null) => void };
const engines = new Map<Engine, SessionMemory>();
export function registerSessionMemory(engine: Engine, memory: SessionMemory) { engines.set(engine, memory); }
export function clearSessionMemory() { clearComposerDrafts(); clearFileDrafts(); for (const memory of engines.values()) memory.clear(); }
export function invalidateSessionMemory(id: string, sequence: number | null) { for (const memory of engines.values()) memory.invalidate(id, sequence); }
