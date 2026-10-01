import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { useI18n } from "../i18n";
import type { CodexModel } from "../codexModels";

export function CodexModelPicker({ value, onChange, models = [], allowServerDefault = false, required = false, requestCustom = 0, loading = false, error = "", onRefresh }: {
  value: string; onChange: (value: string) => void; models?: CodexModel[]; allowServerDefault?: boolean; required?: boolean; requestCustom?: number; loading?: boolean; error?: string; onRefresh?: () => void;
}) {
  const { t } = useI18n();
  const [customMode, setCustomMode] = useState(!allowServerDefault && !value);
  useEffect(() => { if (requestCustom) setCustomMode(true); }, [requestCustom]);
  return <div className="codex-model-picker">
    <select aria-label={t("codex.modelOverride")} value={customMode ? "__custom__" : value} required={required} onChange={event => {
      const custom = event.target.value === "__custom__";
      onChange(custom ? "" : event.target.value);
      setCustomMode(custom);
    }}>
      {allowServerDefault && <option value="">{t("codex.modelServerDefault")}</option>}
      {value && !models.some(option => option.model === value) && <option value={value}>{value}</option>}
      {models.map(option => <option value={option.model} key={option.model}>{option.displayName || option.model}</option>)}
      <option value="__custom__">{t("codex.modelCustom")}</option>
    </select>
    {customMode && <input aria-label={t("codex.customModelName")} value={value} onChange={event => onChange(event.target.value)} placeholder={t("codex.customModelPlaceholder")} required={required} />}
    {onRefresh && <button type="button" className="icon-button" disabled={loading} title={t("codex.refreshModels")} aria-label={t("codex.refreshModels")} onClick={onRefresh}><RefreshCw size={16} className={loading ? "spin" : undefined} /></button>}
    {error && <small role="status">{t("codex.modelsUnavailable")}: {error}</small>}
  </div>;
}
