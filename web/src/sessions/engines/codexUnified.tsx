import { ScheduledTaskDialog } from "../../pages/ScheduledTaskDialog";
import type { ScheduleProps } from "../engineModules";
import { clearCodexComposerPreferences, loadCodexComposerPreferences, saveCodexComposerPreferences } from "../../codexComposerPreferences";
import type { ConversationProps, NativeCreateProps } from "../engineModules";
import type { AISession } from "../types";
import type { Thread } from "../../types";
import { SessionView, CreateThread } from "./codex/SessionView";
import { clearCodexSessionMemory } from "./codex/useThreadEvents";

export function afterFork(sourceID: string, targetID: string) { saveCodexComposerPreferences(targetID, loadCodexComposerPreferences(sourceID)); }
export function afterDelete(id: string) { clearCodexComposerPreferences(id); clearCodexSessionMemory(id); }
export function Create(props: NativeCreateProps) {
  return <CreateThread onBusyChange={props.onBusyChange} fixedWorkspace={props.fixedWorkspace} disabled={props.disabled} initialWorkspaceID={props.workspaceID} workspaces={props.workspaces} onCreated={thread => props.onCreated({ ...thread, engine: "codex" } as AISession)} />;
}
export function Conversation(props: ConversationProps) {
  const stream = props.streamRevisions[props.thread.id];
  return <><section className="session-panel"><SessionView thread={props.thread as Thread} approvals={props.approvals.filter(item => item.thread_id === props.thread.id)} realtime={stream?.revision ?? 0} globalStreamRevision={props.streamRevisions["*"]?.revision ?? 0} streamRevision={stream?.revision ?? 0} invalidationSequence={stream?.minimumSequence} reloadApprovals={props.reloadApprovals} notify={props.notify} onOpenFile={props.onOpenFile} onNewTask={props.onNew} onFork={props.onFork} forkBusy={props.busy} /></section>

  </>;
}

export function Schedule(props: ScheduleProps) { return <ScheduledTaskDialog open agentKind="codex" initialThreadID={props.thread.id} lockThread threads={[props.thread as Thread]} notify={props.notify} onClose={props.onClose} onSaved={props.onSaved} />; }
