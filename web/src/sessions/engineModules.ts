import { lazy, type ComponentType } from "react";
import { Bot, Code2, Sparkles } from "lucide-react";
import type { ScheduledNativeValue } from "./scheduledTaskFields";
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
export type EngineModule = { Conversation: ComponentType<ConversationProps>; Create: ComponentType<NativeCreateProps>; afterFork?: (sourceID: string, targetID: string) => void; afterDelete?: (id: string) => void };
const loaders: Record<Engine, () => Promise<EngineModule>> = {
  codex: () => import("./engines/codexUnified"),
  gemini: () => import("./engines/geminiUnified"),
  claude: () => import("./engines/claudeUnified"),
};
const scheduleFields = {
  codex: () => import("./engines/codexScheduleFields"),
  gemini: () => import("./engines/geminiScheduleFields"),
  claude: () => import("./engines/claudeScheduleFields"),
};
export const engines = (["codex", "gemini", "claude"] as const).map(id => ({
  id, label: { codex: "Codex", gemini: "Gemini", claude: "Claude" }[id],
  Icon: { codex: Code2, gemini: Sparkles, claude: Bot }[id],
  load: loaders[id],
  archiveProjectPath: (projectID: string) => `/projects/${projectID}/${id === "codex" ? "" : `${id}/`}threads/archive`,
  Conversation: lazy(() => loaders[id]().then(module => ({ default: module.Conversation }))),
  ScheduleFields: lazy(scheduleFields[id]),
  schedulePayload: (form: ScheduledNativeValue) => id === "gemini" ? {thinking_level:form.thinking_level} : {},
  scheduleApprovalMode: {codex:"on-request",gemini:"default",claude:"default"}[id],
  Create: lazy(() => loaders[id]().then(module => ({ default: module.Create }))),
}));
export const engineModule = (id: Engine) => engines.find(item => item.id === id)!;
