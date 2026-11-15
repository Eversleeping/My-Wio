import { Field } from "../../components/FormUI";
import { useI18n } from "../../i18n";
import type { ScheduledFieldsProps } from "../scheduledTaskFields";
import { useEffect } from "react";
import { CodexModelPicker } from "../../components/CodexModelPicker";
import { compatibleReasoningEffort, reasoningOptionsFor, useCodexModels } from "../../codexModels";
export default function CodexScheduleFields({form, update, busy, thread, open}: ScheduledFieldsProps) {
  const {t} = useI18n();
  const catalogue = useCodexModels(open ? thread?.workspace_id : undefined, false);
  const codexReasoningOptions = reasoningOptionsFor(catalogue.models, form.model);
  useEffect(() => {
    const next = compatibleReasoningEffort(catalogue.models, form.model, form.reasoning_effort);
    if (next !== form.reasoning_effort) update({reasoning_effort:next});
  }, [catalogue.models, form.model, form.reasoning_effort]);
  return <>
    <div className="form-grid"><Field label={t("codex.modelOverride")}><CodexModelPicker models={catalogue.models} loading={catalogue.loading} error={catalogue.error} value={form.model} onChange={model => update({ model })} allowServerDefault /></Field><Field label={t("codex.reasoningEffort")}><select value={form.reasoning_effort} disabled={busy} onChange={event => update({ reasoning_effort: event.target.value })}><option value="">{t("codex.reasoningDefault")}</option>{codexReasoningOptions.map(option => <option value={option.value} key={option.value}>{option.labelKey ? t(option.labelKey) : option.value}</option>)}</select></Field></div>
    <div className="form-grid"><Field label={t("codex.approveOnRequest")}><select value={form.approval_mode} disabled={busy} onChange={event => update({ approval_mode: event.target.value })}><option value="on-request">{t("codex.approveOnRequest")}</option><option value="untrusted">{t("codex.untrusted")}</option><option value="never">{t("codex.neverApprove")}</option></select></Field><label className="toggle-row"><input type="checkbox" checked={form.enabled} disabled={busy} onChange={event => update({ enabled: event.target.checked })} /><span>{t("settings.scheduleEnabled")}</span></label></div>
</>;
}
