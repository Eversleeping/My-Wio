import { useEffect, useState } from "react";
import { AlertTriangle, Check, Copy, LoaderCircle, RefreshCw, SquareTerminal } from "lucide-react";
import { registrations, type RegistrationCommand, type ServerRegistration } from "../data/registrations";
import { useVisibleClock } from "../hooks/useVisibleInterval";
import { useResource } from "../data/resources";
import { useI18n } from "../i18n";
import { Dialog, DialogActions } from "./Dialog";
import { Section } from "./PageUI";

const terminal = new Set(["complete", "revoked", "expired"]);
export function ServerRegistrations({created, onDismiss, notify}: {created: RegistrationCommand | null; onDismiss: () => void; notify: (text: string) => void}) {
  const { t } = useI18n();
  const rows = useResource(registrations.list());
  const [selected, setSelected] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { if (created) {setSelected(created.registration.id); setError(""); rows.reload();} }, [created]);
  const active = (rows.data ?? []).some(item => !terminal.has(item.state) && new Date(item.state === "pending" ? item.expires_at : item.session_expires_at).getTime() > Date.now());
  const now = useVisibleClock(active, 1000);
  const effective = (item: ServerRegistration): ServerRegistration => !terminal.has(item.state) && new Date(item.state === "pending" ? item.expires_at : item.session_expires_at).getTime() <= now ? {...item, state: "expired"} : item;
  const items = (rows.data ?? []).map(effective);
  const row = items.find(item => item.id === selected) ?? (created?.registration.id === selected ? effective(created.registration) : null);
  const secret = created?.registration.id === selected ? created : null;
  const close = () => {if (busy) return; setSelected(null); setError(""); onDismiss();};
  const revoke = async () => {
    if (!row) return;
    setBusy(true); setError("");
    try {await registrations.revoke(row.id); onDismiss(); rows.reload();}
    catch (failure) {setError(failure instanceof Error ? failure.message : t("server.error.installation_failed"));}
    finally {setBusy(false);}
  };
  const copy = async (value: string) => {
    try {await navigator.clipboard.writeText(value); notify(t("server.commandCopied"));}
    catch {setError(t("server.registration.copyFailed"));}
  };
  return <>
    {(items.length > 0 || rows.error) && <Section title={t("server.registration.history")} icon={<SquareTerminal size={18} />} action={<button className="secondary-button" onClick={rows.reload}><RefreshCw size={15} />{t("common.refresh")}</button>}>
      {rows.error && <p role="alert" className="error-banner">{rows.error}</p>}
      <div className="registration-history">{items.map(item => <button key={item.id} className="secondary-button" onClick={() => {setSelected(item.id);setError("");}}><span>{item.name}</span><span>{t(`server.registration.state.${item.state}`)}</span></button>)}</div>
    </Section>}
    <Dialog open={selected !== null && row !== null} title={t("server.registration.title")} onClose={close} closeLabel={t("common.close")} dismissDisabled={busy} wide>
      {row && <div className="enrollment-step registration-progress">
        {error && <p role="alert" className="error-banner">{error}</p>}
        <strong>{row.name} · {t(`server.registration.state.${row.state}`)}</strong>
        {secret && row.state === "pending" && <>
          <p className="muted">{t("server.registration.instructions")}</p>
          <label className="field"><span>{t("server.command")}</span><textarea className="registration-command" rows={5} readOnly value={secret.command} /></label>
          <button type="button" className="secondary-button" onClick={() => void copy(secret.command)}><Copy size={15} />{t("server.registration.copyCommand")}</button>
          <label className="field"><span>{t("server.registration.code")}</span><input readOnly autoComplete="off" value={secret.code} /></label>
          <button type="button" className="secondary-button" onClick={() => void copy(secret.code)}><Copy size={15} />{t("server.registration.copyCode")}</button>
        </>}
        {!secret && row.state === "pending" && <p className="security-notice">{t("server.registration.codeOnce")}</p>}
        <p className="muted">{t("server.expires", {time: new Date(row.state === "pending" ? row.expires_at : row.session_expires_at).toLocaleString()})}</p>
        {!terminal.has(row.state) && <p className="muted">{t("server.registration.revokeNotice")}</p>}
        {row.error_code && <p role="alert" className="error-banner">{t(`server.registration.error.${row.error_code}`)}</p>}
        {row.state === "failed" && <p className="security-notice">{t("server.registration.retry")}</p>}
        {row.progress.length > 0 && <div className="install-log" aria-live="polite"><div className="install-log-heading"><SquareTerminal size={16} /><strong>{t("server.installLog")}</strong></div><div className="install-log-lines">{row.progress.map((entry, index) => <div className="install-log-entry" key={entry.step}>{row.state === "failed" && index === row.progress.length - 1 ? <AlertTriangle size={15} /> : !terminal.has(row.state) && index === row.progress.length - 1 ? <LoaderCircle className="spin" size={15} /> : <Check size={15} />}<span>{t(`server.progress.${entry.step}`)}</span>{Boolean(entry.total) && <code>{Math.min(100, Math.round(((entry.current ?? 0) / entry.total!) * 100))}%</code>}</div>)}</div></div>}
        {row.result.warnings && row.result.warnings.length > 0 && <div className="warning-list"><strong>{t("server.warningTitle")}</strong>{row.result.warnings.map(warning => <span key={warning}><AlertTriangle size={15} />{t(`server.warning.${warning}`)}</span>)}</div>}
        <DialogActions><button type="button" className="secondary-button" onClick={rows.reload}><RefreshCw size={15} />{t("common.refresh")}</button>{!terminal.has(row.state) && <button type="button" className="secondary-button danger" disabled={busy} onClick={() => void revoke()}>{t("server.registration.revoke")}</button>}<button type="button" className="primary-button" disabled={busy} onClick={close}>{t("common.close")}</button></DialogActions>
      </div>}
    </Dialog>
  </>;
}
