import { Field } from "../../components/FormUI";
import { useI18n } from "../../i18n";
import type { ScheduledFieldsProps } from "../scheduledTaskFields";
export default function ClaudeScheduleFields({form, update, busy}: ScheduledFieldsProps) {
  const {t} = useI18n();
  return <>
     <div className="form-grid"><Field label={t("claude.model")}><input value={form.model} maxLength={128} placeholder={t("settings.followSessionModel")} disabled={busy} onChange={e=>update({model:e.target.value})} /></Field><Field label={t("claude.effort")}><select value={form.reasoning_effort} disabled={busy} onChange={e=>update({reasoning_effort:e.target.value})}>{["","low","medium","high","xhigh","max"].map(value=><option key={value} value={value}>{t(`claude.effort.${value || "default"}`)}</option>)}</select></Field></div>
     <div className="form-grid"><Field label={t("claude.permission")}><select value={form.approval_mode} disabled={busy} onChange={e=>update({approval_mode:e.target.value})}>{["default","acceptEdits","plan","bypassPermissions"].map(value=><option key={value} value={value}>{t(`claude.permission.${value}`)}</option>)}</select></Field><label className="toggle-row"><input type="checkbox" checked={form.enabled} disabled={busy} onChange={e=>update({enabled:e.target.checked})}/><span>{t("settings.scheduleEnabled")}</span></label></div>
</>;
}
