import { lazy, type ComponentType } from "react";
import { Bot, Code2, Sparkles } from "lucide-react";
import type { FilePreviewSelection } from "../codexContent";
import type { CodexPageProps, Workspace } from "../types";
import type { AISession, Engine } from "./types";

export type ConversationProps = Pick<CodexPageProps, "approvals" | "approvalSignal" | "reloadApprovals" | "streamRevisions" | "notify"> & {
  thread: AISession; revision: string; onChanged: () => void;
  onOpenFile: (value: FilePreviewSelection) => void; onNew: () => void;
  onFork: (eventID?: string) => Promise<void>; onSchedule: () => void;
  onArchive: () => void; busy: boolean;
};
export type NativeCreateProps = {
  onBusyChange?: (busy: boolean) => void; fixedWorkspace?: boolean; disabled?: boolean; defaults?: import("./availability").CreateDefaults;
  workspaceID: string; workspaces: Workspace[]; history: AISession[];
  onCreated: (thread: AISession) => void;
};
export type ScheduleProps = { thread: AISession; notify: (text: string) => void; onClose: () => void; onSaved: () => void };
export type EngineModule = { Schedule: ComponentType<ScheduleProps>; Conversation: ComponentType<ConversationProps>; Create: ComponentType<NativeCreateProps>; afterFork?: (sourceID: string, targetID: string) => void; afterDelete?: (id: string) => void };
const loaders: Record<Engine, () => Promise<EngineModule>> = {
  codex: () => import("./engines/codexUnified"),
  gemini: () => import("./engines/geminiUnified"),
  claude: () => import("./engines/claudeUnified"),
};
export const engines = (["codex", "gemini", "claude"] as const).map(id => ({
  id, label: { codex: "Codex", gemini: "Gemini", claude: "Claude" }[id],
  Icon: { codex: Code2, gemini: Sparkles, claude: Bot }[id],
  load: loaders[id],
  archiveProjectPath: (projectID: string) => `/projects/${projectID}/${id === "codex" ? "" : `${id}/`}threads/archive`,
  Conversation: lazy(() => loaders[id]().then(module => ({ default: module.Conversation }))),
  Schedule: lazy(() => loaders[id]().then(module => ({ default: module.Schedule }))),
  Create: lazy(() => loaders[id]().then(module => ({ default: module.Create }))),
}));
export const engineModule = (id: Engine) => engines.find(item => item.id === id)!;
