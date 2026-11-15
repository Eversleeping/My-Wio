import { ScheduledTaskDialog } from "../../pages/ScheduledTaskDialog";
import type { ScheduleProps } from "../engineModules";
import { useEffect, useRef, useState } from "react";
import { post } from "../../api";
import { useClaudeModels } from "../../claudeModels";
import { DialogActions } from "../../components/Dialog";
import { ErrorBanner, Field } from "../../components/FormUI";
import { useI18n } from "../../i18n";
import { message } from "../../text";
import type { ClaudeThread, Server } from "../../types";
import { useData } from "../../useData";
import type { ConversationProps, NativeCreateProps } from "../engineModules";
import type { AISession } from "../types";
import { ClaudeSession, ClaudeOptions } from "./claude";
export function Create(props: NativeCreateProps) {
  const { t } = useI18n();
  const [workspaceID, setWorkspaceID] = useState(props.workspaceID);
  const servers = useData<Server[]>("/servers", 0);
  const server = servers.data?.find(item => item.id === props.workspaces.find(w => w.id === workspaceID)?.server_id);
  const models = useClaudeModels(workspaceID, "create");
  const previous = props.history.find(item => item.workspace_id === workspaceID && item.engine === "claude") as AISession<"claude"> | undefined;
  const [model, setModel] = useState(server?.claude_profile_model || "sonnet");
  const modelSelected = useRef(false);
  const [effort, setEffort] = useState(props.defaults?.effort ?? previous?.effort ?? "");
  const [mode, setMode] = useState(props.defaults?.mode || previous?.permission_mode || "default");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { if (!modelSelected.current) setModel(models.data?.default_model || server?.claude_profile_model || "sonnet"); }, [models.data, server?.claude_profile_model]);
  return <form onSubmit={async event => {
    event.preventDefault(); if (busy || props.disabled || models.loading || !workspaceID || !model.trim()) return; setBusy(true); props.onBusyChange?.(true); setError("");
    try { props.onCreated({ ...await post<ClaudeThread>("/sessions/claude", { workspace_id: workspaceID, model: model.trim(), permission_mode: mode, effort }), engine: "claude" } as AISession); }
    catch (error) { setError(message(error)); } finally { setBusy(false); props.onBusyChange?.(false); }
  }}>
    {!props.fixedWorkspace && <Field label={t("gemini.workspace")}><select required value={workspaceID} disabled={busy || Boolean(props.disabled)} onChange={event => { setWorkspaceID(event.target.value); modelSelected.current = false; setModel("sonnet"); setEffort(""); }}><option value="">{t("gemini.selectWorkspace")}</option>{props.workspaces.filter(w => w.status === "ready").map(w => <option key={w.id} value={w.id}>{w.project_name} · {w.server_name} · {w.path}</option>)}</select></Field>}
    <ClaudeOptions availableModels={models.models} model={model} mode={mode} effort={effort} setModel={value => { modelSelected.current = true; setModel(value); }} setMode={setMode} setEffort={setEffort} disabled={busy || Boolean(props.disabled)} effortSupported={Boolean(server?.claude_effort_supported)} />
    <p className="gemini-hint">{t("claude.permissionsHelp")}</p>{error && <ErrorBanner text={error} />}
    <DialogActions><button className="primary-button" disabled={busy || props.disabled || models.loading || !workspaceID || !model.trim()}>{t("claude.newSession")}</button></DialogActions>
  </form>;
}
export function Conversation(props: ConversationProps) {
  return <ClaudeSession thread={props.thread as ClaudeThread} revision={props.revision} onChanged={props.onChanged} onOpenFile={props.onOpenFile} onArchive={props.onArchive} onSchedule={props.onSchedule} onFork={props.onFork} onNew={props.onNew} notify={props.notify} />;
}

export function Schedule(props: ScheduleProps) { return <ScheduledTaskDialog open agentKind="claude" initialThreadID={props.thread.id} lockThread threads={[]} claudeThreads={[props.thread as ClaudeThread]} notify={props.notify} onClose={props.onClose} onSaved={props.onSaved} />; }
