import { Field } from "../../components/FormUI";
import { useI18n } from "../../i18n";
import type { ScheduledFieldsProps } from "../scheduledTaskFields";
import { useEffect, useState } from "react";
import { GeminiThinkingPicker, compatibleGeminiThinking } from "../../components/GeminiThinkingPicker";
import { useResource } from "../../data/resources";
import { settings } from "../../data/settings";
export default function GeminiScheduleFields({form, update, busy, thread, open}: ScheduledFieldsProps) {
  const {t} = useI18n();
  const geminiModels = useResource(open && thread?.workspace_id ? settings.geminiModels(thread.workspace_id) : null);
  const [customGeminiModel,setCustomGeminiModel] = useState(false);
  useEffect(() => { if (open) setCustomGeminiModel(false); }, [open, thread?.id]);
  return <>
     <Field label={t("gemini.model")}><select value={customGeminiModel || (form.model && !(geminiModels.data?.models ?? []).some(m=>m.id===form.model)) ? "__custom__" : form.model} disabled={busy} onChange={e=>{setCustomGeminiModel(e.target.value==="__custom__");update({model:e.target.value==="__custom__"?"":e.target.value,thinking_level:""});}}><option value="">{t("settings.followSessionModel")}</option>{(geminiModels.data?.models??[]).map(m=><option value={m.id} key={m.id}>{m.name===m.id?m.id:`${m.name} · ${m.id}`}</option>)}<option value="__custom__">{t("gemini.customModel")}</option></select>{(customGeminiModel || Boolean(form.model && !(geminiModels.data?.models??[]).some(m=>m.id===form.model))) && <input maxLength={128} value={form.model} onChange={e=>update({model:e.target.value,thinking_level:compatibleGeminiThinking(e.target.value,form.thinking_level)})} aria-label={t("gemini.customModelName")} />}</Field>
     <GeminiThinkingPicker model={form.model || thread?.model || ""} value={form.thinking_level} onChange={thinking_level=>update({thinking_level})} disabled={busy} followSession />
     <div className="form-grid"><Field label={t("gemini.permissions")}><select value={form.approval_mode} disabled={busy} onChange={e=>update({approval_mode:e.target.value})}><option value="default">{t("gemini.defaultMode")}</option><option value="auto_edit">{t("gemini.autoEditMode")}</option><option value="yolo">{t("gemini.neverApprove")}</option></select></Field><label className="toggle-row"><input type="checkbox" checked={form.enabled} disabled={busy} onChange={e=>update({enabled:e.target.checked})}/><span>{t("settings.scheduleEnabled")}</span></label></div>
</>;
}
