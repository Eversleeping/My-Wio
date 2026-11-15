import type { ConversationProps, NativeCreateProps } from "../engineModules";
import type { AISession } from "../types";
import type { GeminiThread } from "../../types";
import { GeminiSession, CreateGeminiSession } from "./gemini";
export function Create(props: NativeCreateProps) {
  const previous = props.history.find(item => item.workspace_id === props.workspaceID && item.engine === "gemini") as AISession<"gemini"> | undefined;
  return <CreateGeminiSession onBusyChange={props.onBusyChange} fixedWorkspace={props.fixedWorkspace} disabled={props.disabled} initialWorkspaceID={props.workspaceID} initialModel={previous?.model ?? props.defaults?.model ?? ""} initialMode={(previous?.approval_mode || props.defaults?.mode) === "plan" ? "default" : previous?.approval_mode || props.defaults?.mode || "default"} initialThinking={previous?.thinking_level ?? props.defaults?.effort ?? ""} workspaces={props.workspaces} error="" onRetry={() => undefined} onCreated={thread => props.onCreated({ ...thread, engine: "gemini" } as AISession)} />;
}
export function Conversation(props: ConversationProps) {
  return <GeminiSession thread={props.thread as GeminiThread} realtime={props.revision} notify={props.notify} onChanged={props.onChanged} onNewSession={props.onNew} onOpenFile={props.onOpenFile} onFork={props.onFork} onSchedule={props.onSchedule} />;
}
