import { CalendarClock, LoaderCircle, Pause, Pencil, Play, Plus, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { DataTable, ErrorState, PageLoading, Section, Status } from "../../components/PageUI";
import { useResource, useResourceRevision } from "../../data/resources";
import { settings as settingEndpoints } from "../../data/settings";
import { formatDate } from "../../format";
import { useI18n } from "../../i18n";
import { engineModule } from "../../sessions/engineModules";
import { useSessionChoices } from "../../sessions/useSessionChoices";
import { message } from "../../text";
import type { ScheduledTask } from "../../types";
import { ScheduledTaskDialog } from "../ScheduledTaskDialog";

/** Every scheduled task across workspaces and engines. */
export function ScheduledTaskSettings({ notify, active }: { notify: (text: string) => void; active: boolean }) {
  const { t } = useI18n();
  const scheduledTasks = useResource(settingEndpoints.scheduledTasks());
  const sessionRevision = useResourceRevision(["settings", "codex", "gemini", "claude"]);
  const sessions = useSessionChoices({}, sessionRevision);
  const [scheduledTarget, setScheduledTarget] = useState<{ task?: ScheduledTask; threadID?: string } | null>(null);
  const [scheduledBusy, setScheduledBusy] = useState("");
  const [settingsConfirmation, setSettingsConfirmation] = useState<{ type: "scheduled-task"; task: ScheduledTask } | null>(null);
  const [settingsDeleteBusy, setSettingsDeleteBusy] = useState("");
  const settingsDeleteBusyRef = useRef("");
  // A category that is no longer shown must not keep a modal and its focus trap open.
  useEffect(() => { if (!active) { setScheduledTarget(null); setSettingsConfirmation(null); } }, [active]);
  const openScheduledTask = (task?: ScheduledTask) => setScheduledTarget(task ? { task } : {});
  const toggleScheduledTask = async (task: ScheduledTask) => {
    if (scheduledBusy) return;
    setScheduledBusy(task.id);
    try { await settingEndpoints.saveScheduledTask(task.id, { enabled: !task.enabled }); } catch (err) { notify(message(err)); } finally { setScheduledBusy(""); }
  };
  const deleteScheduledTask = async (task: ScheduledTask) => {
    if (scheduledBusy || settingsDeleteBusyRef.current) return;
    const busyKey = `scheduled-task:${task.id}`;
    settingsDeleteBusyRef.current = busyKey;
    setSettingsDeleteBusy(busyKey);
    try { await settingEndpoints.removeScheduledTask(task.id); notify(t("settings.scheduledTaskDeleted")); setSettingsConfirmation(null); } catch (err) { notify(message(err)); } finally { settingsDeleteBusyRef.current = ""; setSettingsDeleteBusy(""); }
  };
  return <>
    <Section title={t("settings.scheduledTasks")} icon={<CalendarClock size={18} />} action={<button className="primary-button" onClick={() => openScheduledTask()}><Plus size={17} />{t("settings.newScheduledTask")}</button>}>
      <DataTable headers={[t("settings.name"), t("settings.scheduledThread"), t("settings.schedule"), t("settings.nextRun"), t("column.state"), ""]} empty={t("settings.noScheduledTasks")}>
        {(scheduledTasks.data ?? []).map(task => <tr key={task.id}><td><div className="cell-main"><strong>{task.name}</strong><small title={task.prompt}>{task.prompt}</small></div></td><td><div className="cell-main"><strong>{engineModule(task.agent_kind ?? "codex").label} · {task.thread_title}</strong><small>{task.project_name} / {task.server_name}</small></div></td><td className="nowrap"><code title={task.timezone}>{task.schedule}</code></td><td>{task.enabled ? formatDate(task.next_run_at) : t("settings.scheduleDisabled")}</td><td><div className="cell-main"><Status value={task.enabled ? "enabled" : "disabled"} />{task.last_run_status && <small title={task.last_run_message || undefined}>{t("settings.lastRun", { status: task.last_run_status })}</small>}</div></td><td><div className="row-actions"><button className="icon-button" title={t("common.edit")} disabled={Boolean(scheduledBusy || settingsDeleteBusy)} onClick={() => openScheduledTask(task)}><Pencil size={15} /></button><button className="icon-button" title={t(task.enabled ? "settings.disableScheduledTask" : "settings.enableScheduledTask")} disabled={Boolean(scheduledBusy || settingsDeleteBusy)} onClick={() => void toggleScheduledTask(task)}>{scheduledBusy === task.id ? <LoaderCircle className="spin" size={15} /> : task.enabled ? <Pause size={15} /> : <Play size={15} />}</button><button className="icon-button danger" title={t("settings.deleteScheduledTask")} disabled={Boolean(scheduledBusy || settingsDeleteBusy)} onClick={() => setSettingsConfirmation({ type: "scheduled-task", task })}><Trash2 size={15} /></button></div></td></tr>)}
      </DataTable>
    </Section>
    <ScheduledTaskDialog open={scheduledTarget !== null} task={scheduledTarget?.task} threads={[]} sessions={sessions.data} sessionNotice={sessions.error ? <ErrorState error={sessions.error} reload={sessions.reload} /> : sessions.loading ? <PageLoading /> : null} notify={notify} onClose={() => setScheduledTarget(null)} onSaved={() => {}} />
    {settingsConfirmation && <ConfirmDialog open danger title={t("settings.deleteScheduledTask")} impact={t("settings.confirmDeleteScheduledTask", { name: settingsConfirmation.task.name })} confirmLabel={t("settings.deleteScheduledTask")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={Boolean(settingsDeleteBusy)} onClose={() => setSettingsConfirmation(null)} onConfirm={() => deleteScheduledTask(settingsConfirmation.task)} />}
  </>;
}
