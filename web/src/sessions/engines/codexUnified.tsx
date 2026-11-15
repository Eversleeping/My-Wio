import { relative } from "../../format";
import { approvalDetail, readableKind } from "./codex";
import { ScheduledTaskDialog } from "../../pages/ScheduledTaskDialog";
import type { ScheduleProps } from "../engineModules";
import { useEffect, useState } from "react";
import { ShieldCheck } from "lucide-react";
import { Dialog } from "../../components/FormUI";
import { Empty, Status } from "../../components/PageUI";
import { UserInputPrompt, isUserInputRequest } from "../../components/UserInputPrompt";
import { useI18n } from "../../i18n";
import { clearCodexComposerPreferences, loadCodexComposerPreferences, saveCodexComposerPreferences } from "../../codexComposerPreferences";
import type { ConversationProps, NativeCreateProps } from "../engineModules";
import type { AISession } from "../types";
import type { Thread } from "../../types";
import { SessionView, CreateThread } from "./codex/SessionView";
import { ApprovalActions } from "./codex/approvals";
import { clearCodexSessionMemory } from "./codex/useThreadEvents";

export function afterFork(sourceID: string, targetID: string) { saveCodexComposerPreferences(targetID, loadCodexComposerPreferences(sourceID)); }
export function afterDelete(id: string) { clearCodexComposerPreferences(id); clearCodexSessionMemory(id); }
export function Create(props: NativeCreateProps) {
  return <CreateThread onBusyChange={props.onBusyChange} fixedWorkspace={props.fixedWorkspace} disabled={props.disabled} initialWorkspaceID={props.workspaceID} workspaces={props.workspaces} onCreated={thread => props.onCreated({ ...thread, engine: "codex" } as AISession)} />;
}
export function Conversation(props: ConversationProps) {
  const { t } = useI18n();
  const [approvalOpen, setApprovalOpen] = useState(false);
  const key = props.approvals.map(item => item.id).join(",");
  useEffect(() => { setApprovalOpen(Boolean(key)); }, [key, props.approvalSignal]);
  const stream = props.streamRevisions[props.thread.id];
  return <><section className="session-panel"><SessionView thread={props.thread as Thread} approvals={props.approvals.filter(item => item.thread_id === props.thread.id)} realtime={stream?.revision ?? 0} globalStreamRevision={props.streamRevisions["*"]?.revision ?? 0} streamRevision={stream?.revision ?? 0} invalidationSequence={stream?.minimumSequence} reloadApprovals={props.reloadApprovals} notify={props.notify} onOpenFile={props.onOpenFile} onNewTask={props.onNew} onFork={props.onFork} forkBusy={props.busy} /></section>
    <button className={`approval-drawer-button ${props.approvals.length ? "visible" : ""}`} onClick={() => setApprovalOpen(true)}><ShieldCheck size={17} />{t("codex.approvalCount", { count: props.approvals.length })}</button>
    <Dialog open={approvalOpen} title={t("codex.pendingApprovals")} onClose={() => setApprovalOpen(false)} wide><div className="approval-list">{props.approvals.length === 0 ? <Empty icon={<ShieldCheck size={24} />} text={t("codex.noApprovals")} /> : props.approvals.map(item => isUserInputRequest(item) ? <UserInputPrompt key={item.id} item={item} onDecided={props.reloadApprovals} /> : <div className="approval-item" key={item.id}><div className="approval-meta"><Status value="pending" /><span>{item.title}</span><time>{relative(item.expires_at)}</time></div><strong>{readableKind(item.kind)}</strong><pre>{approvalDetail(item.detail)}</pre><ApprovalActions item={item} onDecided={props.reloadApprovals} notify={props.notify} /></div>)}</div></Dialog>
  </>;
}

export function Schedule(props: ScheduleProps) { return <ScheduledTaskDialog open agentKind="codex" initialThreadID={props.thread.id} lockThread threads={[props.thread as Thread]} notify={props.notify} onClose={props.onClose} onSaved={props.onSaved} />; }
