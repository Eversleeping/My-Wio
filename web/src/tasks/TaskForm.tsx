import { useEffect, useRef, useState, type ReactNode } from "react";
import { Dialog, DialogActions } from "../components/Dialog";
import { TaskRequestPending } from "../data/tasks";
import { useI18n } from "../i18n";

export function TaskForm({ title, submitLabel, onClose, onSuccess = onClose, prepare, children, disabled = false }: {
  title: string; submitLabel: string; onClose: () => void;
  onSuccess?: () => void;
  prepare: () => () => Promise<void>;
  children: ReactNode; disabled?: boolean;
}) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [pending, setPending] = useState("");
  const retry = useRef<(() => Promise<void>) | null>(null);
  const live = useRef(true);
  useEffect(() => { live.current = true; return () => { live.current = false; }; }, []);

  const submit = async () => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      retry.current ??= prepare();
      await retry.current();
      if (live.current) onSuccess();
    } catch (err) {
      if (!live.current) return;
      if (err instanceof TaskRequestPending) {
        setPending(err.requestID);
      } else {
        retry.current = null;
        setPending("");
        setError(err instanceof Error ? err.message : t("error.load"));
      }
    } finally {
      if (live.current) setBusy(false);
    }
  };

  return <Dialog open wide title={title} onClose={onClose} closeLabel={t("common.close")} dismissDisabled={busy || Boolean(pending)}>
    <form className="task-form" onSubmit={event => { event.preventDefault(); void submit(); }}>
      <fieldset disabled={busy || Boolean(pending)}>{children}</fieldset>
      {error && <p className="form-error" role="alert">{error}</p>}
      {pending && <p className="form-error" role="alert">{t("tasks.requestPending", { id: pending })}</p>}
      <DialogActions>
        <button type="button" className="secondary-button" onClick={onClose} disabled={busy || Boolean(pending)}>{t("common.cancel")}</button>
        <button className="primary-button" disabled={busy || (disabled && !pending)}>{t(pending ? "common.retry" : submitLabel)}</button>
      </DialogActions>
    </form>
  </Dialog>;
}
