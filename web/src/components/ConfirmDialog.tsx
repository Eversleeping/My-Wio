import { AlertTriangle, LoaderCircle } from "lucide-react";
import { type ReactNode, useId } from "react";
import { Dialog, DialogActions } from "./Dialog";

interface ConfirmDialogCommonProps {
  open: boolean;
  title: ReactNode;
  description?: ReactNode;
  confirmLabel: string;
  cancelLabel: string;
  onConfirm: () => void | Promise<void>;
  onClose: () => void;
  busy?: boolean;
  closeLabel?: string;
  className?: string;
}

/**
 * A dangerous confirmation must describe its impact so callers make the
 * affected scope visible before the user commits the operation.
 */
type ConfirmDialogImpact = Exclude<ReactNode, boolean | null | undefined>;

export type ConfirmDialogProps = ConfirmDialogCommonProps & (
  | { danger?: false; impact?: ConfirmDialogImpact }
  | { danger: true; impact: ConfirmDialogImpact }
);

/**
 * Reusable confirmation dialog for replacing synchronous browser confirms.
 * Labels are supplied by the caller so they can be localized with the action.
 */
export function ConfirmDialog({
  open,
  title,
  description,
  impact,
  confirmLabel,
  cancelLabel,
  onConfirm,
  onClose,
  danger = false,
  busy = false,
  closeLabel,
  className = ""
}: ConfirmDialogProps) {
  const descriptionID = useId();
  const hasImpact = impact !== undefined && impact !== null;
  const hasDescription = Boolean(description) || hasImpact;
  const close = () => {
    if (!busy) onClose();
  };

  const confirm = () => {
    if (!busy) void onConfirm();
  };

  return <Dialog
    open={open}
    title={title}
    onClose={close}
    closeLabel={closeLabel}
    dismissDisabled={busy}
    ariaDescribedBy={hasDescription ? descriptionID : undefined}
    className={`confirm-dialog-shell ${danger ? "danger" : ""} ${className}`.trim()}
  >
    <div className={`confirm-dialog ${danger ? "confirm-dialog-danger" : ""}`.trim()} aria-busy={busy || undefined}>
      {hasDescription && <div id={descriptionID}>
        {description && <p className="confirm-dialog-description">{description}</p>}
        {hasImpact && <div className={`confirm-dialog-impact ${danger ? "error-banner" : ""}`.trim()} role={danger ? "alert" : "note"}>
          {danger && <AlertTriangle size={16} aria-hidden="true" />}
          <div>{impact}</div>
        </div>}
      </div>}
      <DialogActions>
        <button type="button" className="secondary-button" disabled={busy} onClick={close} autoFocus>{cancelLabel}</button>
        <button type="button" className={`primary-button ${danger ? "danger" : ""}`.trim()} disabled={busy} onClick={confirm} aria-label={confirmLabel} aria-busy={busy || undefined}>
          {busy && <LoaderCircle className="spin" size={16} aria-hidden="true" />}
          {confirmLabel}
        </button>
      </DialogActions>
    </div>
  </Dialog>;
}
