import { ScheduledTaskDialog } from "../../pages/ScheduledTaskDialog";
import type { ScheduleProps } from "../engineModules";
import type { ConversationProps, NativeCreateProps } from "../engineModules";
import type { AISession } from "../types";
import type { GeminiThread } from "../../types";
import { GeminiSession, CreateGeminiSession } from "./gemini";
export function Create(props: NativeCreateProps) {
  const previous = props.history.find(item => item.workspace_id === props.workspaceID && item.engine === "gemini") as AISession<"gemini"> | undefined;
  return <CreateGeminiSession initialWorkspaceID={props.workspaceID} initialModel={previous?.model ?? ""} initialMode={previous?.approval_mode === "plan" ? "default" : previous?.approval_mode ?? "default"} initialThinking={previous?.thinking_level ?? ""} workspaces={props.workspaces} error="" onRetry={() => undefined} onCreated={thread => props.onCreated({ ...thread, engine: "gemini" } as AISession)} />;
}
export function Conversation(props: ConversationProps) {
  return <GeminiSession thread={props.thread as GeminiThread} realtime={props.revision} notify={props.notify} onChanged={props.onChanged} onNewSession={props.onNew} onOpenFile={props.onOpenFile} onFork={props.onFork} onSchedule={props.onSchedule} />;
}

export function Schedule(props: ScheduleProps) { return <ScheduledTaskDialog open agentKind="gemini" initialThreadID={props.thread.id} lockThread threads={[]} geminiThreads={[props.thread as GeminiThread]} notify={props.notify} onClose={props.onClose} onSaved={props.onSaved} />; }
