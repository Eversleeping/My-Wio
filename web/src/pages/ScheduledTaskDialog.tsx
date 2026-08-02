import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { Check, LoaderCircle } from "lucide-react";
import { post, put } from "../api";
import { Dialog as AccessibleDialog, DialogActions, type DialogProps } from "../components/Dialog";
import { defaultCodexComposerPreferences } from "../codexComposerPreferences";
import { useI18n } from "../i18n";
import type { ScheduledTask, Thread } from "../types";

const defaultCodexModel = defaultCodexComposerPreferences.model;
const codexModelOptions = [
  { value: "gpt-5.6-sol", labelKey: "codex.model56Sol" },
  { value: "gpt-5.6-terra", labelKey: "codex.model56Terra" },
  { value: "gpt-5.6-luna", labelKey: "codex.model56Luna" },
  { value: "gpt-5.5", labelKey: "codex.model55" }
] as const;
const codexReasoningOptions = [
  { value: "low", labelKey: "codex.reasoningLow" },
  { value: "medium", labelKey: "codex.reasoningMedium" },
  { value: "high", labelKey: "codex.reasoningHigh" },
  { value: "xhigh", labelKey: "codex.reasoningExtraHigh" },
  { value: "max", labelKey: "codex.reasoningMax" }
] as const;

function message(error: unknown) {
  return error instanceof Error ? error.message : "Request failed";
}
function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}</label>;
}

function Dialog(props: Omit<DialogProps, "closeLabel">) {
  const { t } = useI18n();
  return <AccessibleDialog {...props} closeLabel={t("common.close")} />;
}

function CodexModelPicker({ value, onChange, allowServerDefault = false, required = false, requestCustom = 0 }: { value: string; onChange: (value: string) => void; allowServerDefault?: boolean; required?: boolean; requestCustom?: number }) {
  const { t } = useI18n();
  const known = value === "" || codexModelOptions.some(option => option.value === value);
  const [customMode, setCustomMode] = useState(!known);
  const [customValue, setCustomValue] = useState(known ? "" : value);
  useEffect(() => { if (known) { setCustomMode(false); setCustomValue(""); } else { setCustomMode(true); setCustomValue(value); } }, [known, value]);
  useEffect(() => { if (requestCustom) { setCustomMode(true); setCustomValue(""); } }, [requestCustom]);
  const selectValue = customMode ? "__custom__" : value;
  return <div className="codex-model-picker"><select aria-label={t("codex.modelOverride")} value={selectValue} required={required} onChange={event => { if (event.target.value === "__custom__") { setCustomMode(true); setCustomValue(""); onChange(""); } else { setCustomMode(false); onChange(event.target.value); } }}>{allowServerDefault && <option value="">{t("codex.modelServerDefault")}</option>}{codexModelOptions.map(option => <option value={option.value} key={option.value}>{t(option.labelKey)}</option>)}<option value="__custom__">{t("codex.modelCustom")}</option></select>{customMode && <input aria-label={t("codex.customModelName")} value={customValue} onChange={event => { setCustomValue(event.target.value); onChange(event.target.value); }} placeholder={t("codex.customModelPlaceholder")} required={required} />}</div>;
}

type ScheduledTaskFrequency = "daily" | "weekdays" | "weekly" | "monthly" | "interval" | "custom";
type ScheduledTaskIntervalUnit = "m" | "h" | "d";

type ScheduledTaskFormValue = {
  id: string;
  thread_id: string;
  name: string;
  prompt: string;
  schedule: string;
  frequency: ScheduledTaskFrequency;
  hour: string;
  minute: string;
  weekday: string;
  monthDay: string;
  intervalValue: string;
  intervalUnit: ScheduledTaskIntervalUnit;
  timezone: string;
  enabled: boolean;
  model: string;
  reasoning_effort: string;
  approval_mode: string;
};

const scheduledHours = Array.from({ length: 24 }, (_, value) => String(value).padStart(2, "0"));
const scheduledMinutes = Array.from({ length: 60 }, (_, value) => String(value).padStart(2, "0"));
const scheduledMonthDays = Array.from({ length: 31 }, (_, value) => String(value + 1));
const scheduledIntervalValues = ["1", "2", "3", "4", "6", "8", "12", "24"];

function defaultScheduledTaskForm(): ScheduledTaskFormValue {
  let timezone = "UTC";
  try { timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || timezone; } catch { /* browser may not expose an IANA timezone */ }
  return { id: "", thread_id: "", name: "", prompt: "", schedule: "0 9 * * *", frequency: "daily", hour: "09", minute: "00", weekday: "1", monthDay: "1", intervalValue: "1", intervalUnit: "h", timezone, enabled: true, model: "", reasoning_effort: "", approval_mode: "on-request" };
}

function scheduledScheduleFields(expression: string): Pick<ScheduledTaskFormValue, "schedule" | "frequency" | "hour" | "minute" | "weekday" | "monthDay" | "intervalValue" | "intervalUnit"> {
  const schedule = expression.trim();
  const lower = schedule.toLowerCase();
  if (lower === "@hourly") return { schedule, frequency: "interval", hour: "09", minute: "00", weekday: "1", monthDay: "1", intervalValue: "1", intervalUnit: "h" };
  if (lower === "@daily") return { schedule, frequency: "daily", hour: "00", minute: "00", weekday: "1", monthDay: "1", intervalValue: "1", intervalUnit: "h" };
  if (lower === "@weekly") return { schedule, frequency: "weekly", hour: "00", minute: "00", weekday: "0", monthDay: "1", intervalValue: "1", intervalUnit: "h" };
  if (lower === "@monthly") return { schedule, frequency: "monthly", hour: "00", minute: "00", weekday: "1", monthDay: "1", intervalValue: "1", intervalUnit: "h" };
  const every = /^@every\s+(\d+)(m|h|d)$/i.exec(schedule);
  if (every) return { schedule, frequency: "interval", hour: "09", minute: "00", weekday: "1", monthDay: "1", intervalValue: every[1], intervalUnit: every[2].toLowerCase() as ScheduledTaskIntervalUnit };
  const parts = schedule.split(/\s+/);
  if (parts.length === 5 && /^\d+$/.test(parts[0]) && /^\d+$/.test(parts[1])) {
    const common = { schedule, hour: parts[1].padStart(2, "0"), minute: parts[0].padStart(2, "0"), weekday: "1", monthDay: "1", intervalValue: "1", intervalUnit: "h" as ScheduledTaskIntervalUnit };
    if (parts[2] === "*" && parts[3] === "*" && parts[4] === "*") return { ...common, frequency: "daily" };
    if (parts[2] === "*" && parts[3] === "*" && parts[4] === "1-5") return { ...common, frequency: "weekdays" };
    if (parts[2] === "*" && parts[3] === "*" && /^\d$/.test(parts[4])) return { ...common, frequency: "weekly", weekday: parts[4] };
    if (/^\d{1,2}$/.test(parts[2]) && parts[3] === "*" && parts[4] === "*") return { ...common, frequency: "monthly", monthDay: parts[2] };
  }
  return { schedule, frequency: "custom", hour: "09", minute: "00", weekday: "1", monthDay: "1", intervalValue: "1", intervalUnit: "h" };
}

function scheduledScheduleExpression(form: ScheduledTaskFormValue): string {
  if (form.frequency === "custom") return form.schedule.trim();
  if (form.frequency === "interval") return `@every ${form.intervalValue}${form.intervalUnit}`;
  const time = `${Number(form.minute)} ${Number(form.hour)}`;
  if (form.frequency === "weekdays") return `${time} * * 1-5`;
  if (form.frequency === "weekly") return `${time} * * ${Number(form.weekday)}`;
  if (form.frequency === "monthly") return `${time} ${Number(form.monthDay)} * *`;
  return `${time} * * *`;
}

function scheduledTaskFormValue(task?: ScheduledTask, initialThreadID = ""): ScheduledTaskFormValue {
  const base = defaultScheduledTaskForm();
  if (!task) return { ...base, thread_id: initialThreadID };
  return { ...base, ...scheduledScheduleFields(task.schedule), id: task.id, thread_id: task.thread_id, name: task.name, prompt: task.prompt, timezone: task.timezone, enabled: task.enabled, model: task.model, reasoning_effort: task.reasoning_effort, approval_mode: task.approval_mode };
}

export function ScheduledTaskDialog({ open, task, initialThreadID, lockThread = false, threads, notify, onClose, onSaved }: { open: boolean; task?: ScheduledTask; initialThreadID?: string; lockThread?: boolean; threads: Thread[]; notify: (text: string) => void; onClose: () => void; onSaved: () => void }) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState<ScheduledTaskFormValue>(() => scheduledTaskFormValue(task, initialThreadID));
  useEffect(() => { if (open) setForm(scheduledTaskFormValue(task, initialThreadID)); }, [initialThreadID, open, task?.id]);
  const update = (changes: Partial<ScheduledTaskFormValue>) => setForm(current => ({ ...current, ...changes }));
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    const schedule = scheduledScheduleExpression(form);
    if (!schedule) return;
    setBusy(true);
    try {
      const payload = { thread_id: form.thread_id, name: form.name, prompt: form.prompt, schedule, timezone: form.timezone, enabled: form.enabled, model: form.model, reasoning_effort: form.reasoning_effort, approval_mode: form.approval_mode };
      if (form.id) await put(`/scheduled-tasks/${form.id}`, payload);
      else await post("/scheduled-tasks", payload);
      onClose();
      onSaved();
      notify(t("settings.scheduledTaskSaved"));
    } catch (error) { notify(message(error)); } finally { setBusy(false); }
  };
  const threadAvailable = threads.some(thread => thread.id === form.thread_id);
  return <Dialog open={open} title={t(form.id ? "settings.editScheduledTask" : "settings.newScheduledTask")} onClose={() => { if (!busy) onClose(); }} wide><form onSubmit={save}>
    <div className="form-grid"><Field label={t("settings.name")}><input value={form.name} onChange={event => update({ name: event.target.value })} maxLength={180} required /></Field><Field label={t("settings.scheduledThread")}><select value={form.thread_id} disabled={lockThread || busy} onChange={event => update({ thread_id: event.target.value })} required><option value="">{t("settings.selectScheduledThread")}</option>{form.thread_id && !threadAvailable && <option value={form.thread_id}>{task?.thread_title || form.thread_id}</option>}{threads.map(thread => <option value={thread.id} key={thread.id}>{thread.title} / {thread.project_name} / {thread.server_name}</option>)}</select></Field></div>
    <Field label={t("settings.prompt")}><textarea rows={5} value={form.prompt} onChange={event => update({ prompt: event.target.value })} maxLength={20000} required /></Field>
    <div className="form-grid"><Field label={t("settings.scheduleFrequency")}><select value={form.frequency} disabled={busy} onChange={event => update({ frequency: event.target.value as ScheduledTaskFrequency })}><option value="daily">{t("settings.scheduleDaily")}</option><option value="weekdays">{t("settings.scheduleWeekdays")}</option><option value="weekly">{t("settings.scheduleWeekly")}</option><option value="monthly">{t("settings.scheduleMonthly")}</option><option value="interval">{t("settings.scheduleInterval")}</option><option value="custom">{t("settings.scheduleCustom")}</option></select></Field><Field label={t("settings.timezone")}><input value={form.timezone} disabled={busy} onChange={event => update({ timezone: event.target.value })} placeholder="Asia/Shanghai" required /></Field></div>
    {(form.frequency === "daily" || form.frequency === "weekdays" || form.frequency === "weekly" || form.frequency === "monthly") && <div className="form-grid"><Field label={t("settings.scheduleHour")}><select value={form.hour} disabled={busy} onChange={event => update({ hour: event.target.value })}>{scheduledHours.map(value => <option value={value} key={value}>{value}</option>)}</select></Field><Field label={t("settings.scheduleMinute")}><select value={form.minute} disabled={busy} onChange={event => update({ minute: event.target.value })}>{scheduledMinutes.map(value => <option value={value} key={value}>{value}</option>)}</select></Field></div>}
    {form.frequency === "weekly" && <Field label={t("settings.scheduleWeekday")}><select value={form.weekday} disabled={busy} onChange={event => update({ weekday: event.target.value })}><option value="1">{t("settings.weekdayMonday")}</option><option value="2">{t("settings.weekdayTuesday")}</option><option value="3">{t("settings.weekdayWednesday")}</option><option value="4">{t("settings.weekdayThursday")}</option><option value="5">{t("settings.weekdayFriday")}</option><option value="6">{t("settings.weekdaySaturday")}</option><option value="0">{t("settings.weekdaySunday")}</option></select></Field>}
    {form.frequency === "monthly" && <Field label={t("settings.scheduleMonthDay")}><select value={form.monthDay} disabled={busy} onChange={event => update({ monthDay: event.target.value })}>{scheduledMonthDays.map(value => <option value={value} key={value}>{value}</option>)}</select></Field>}
    {form.frequency === "interval" && <div className="form-grid"><Field label={t("settings.scheduleIntervalValue")}><select value={form.intervalValue} disabled={busy} onChange={event => update({ intervalValue: event.target.value })}>{!scheduledIntervalValues.includes(form.intervalValue) && <option value={form.intervalValue}>{form.intervalValue}</option>}{scheduledIntervalValues.map(value => <option value={value} key={value}>{value}</option>)}</select></Field><Field label={t("settings.scheduleIntervalUnit")}><select value={form.intervalUnit} disabled={busy} onChange={event => update({ intervalUnit: event.target.value as ScheduledTaskIntervalUnit })}><option value="m">{t("settings.scheduleMinutes")}</option><option value="h">{t("settings.scheduleHours")}</option><option value="d">{t("settings.scheduleDays")}</option></select></Field></div>}
    {form.frequency === "custom" && <Field label={t("settings.scheduleExpression")}><input value={form.schedule} disabled={busy} onChange={event => update({ schedule: event.target.value })} placeholder={t("settings.schedulePlaceholder")} maxLength={100} required /></Field>}
    <div className="form-grid"><Field label={t("codex.modelOverride")}><CodexModelPicker value={form.model} onChange={model => update({ model })} allowServerDefault /></Field><Field label={t("codex.reasoningEffort")}><select value={form.reasoning_effort} disabled={busy} onChange={event => update({ reasoning_effort: event.target.value })}><option value="">{t("codex.reasoningDefault")}</option>{codexReasoningOptions.map(option => <option value={option.value} key={option.value}>{t(option.labelKey)}</option>)}</select></Field></div>
    <div className="form-grid"><Field label={t("codex.approveOnRequest")}><select value={form.approval_mode} disabled={busy} onChange={event => update({ approval_mode: event.target.value })}><option value="on-request">{t("codex.approveOnRequest")}</option><option value="untrusted">{t("codex.untrusted")}</option><option value="never">{t("codex.neverApprove")}</option></select></Field><label className="toggle-row"><input type="checkbox" checked={form.enabled} disabled={busy} onChange={event => update({ enabled: event.target.checked })} /><span>{t("settings.scheduleEnabled")}</span></label></div>
    <DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{t("common.cancel")}</button><button className="primary-button" disabled={busy || !form.thread_id || !form.name.trim() || !form.prompt.trim()}>{busy ? <LoaderCircle className="spin" size={16} /> : <Check size={16} />}{t("common.save")}</button></DialogActions>
  </form></Dialog>;
}
