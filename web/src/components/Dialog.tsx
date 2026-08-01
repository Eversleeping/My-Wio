import { type MouseEvent as ReactMouseEvent, type ReactNode, useId, useLayoutEffect, useRef } from "react";
import { X } from "lucide-react";

export interface DialogProps {
  open: boolean;
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
  className?: string;
  /** Accessible label for the close button. Defaults to the generic "Close". */
  closeLabel?: string;
}

export interface DialogActionsProps {
  children: ReactNode;
}

type ActiveDialog = { element: HTMLElement; order: number };

const activeDialogs = new Set<ActiveDialog>();
let dialogOrder = 0;

const focusableSelector = [
  "a[href]",
  "area[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type='hidden'])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "iframe",
  "object",
  "embed",
  "[contenteditable]:not([contenteditable='false'])",
  "[tabindex]:not([tabindex='-1'])"
].join(",");

function getTopDialog() {
  const dialogs = [...activeDialogs].filter(dialog => dialog.element.isConnected);
  return dialogs.reduce<ActiveDialog | undefined>((top, candidate) => {
    if (!top || top.element.contains(candidate.element)) return candidate;
    if (candidate.element.contains(top.element)) return top;
    return candidate.order > top.order ? candidate : top;
  }, undefined);
}

function isTopDialog(element: HTMLElement | null) {
  return element !== null && getTopDialog()?.element === element;
}

function getFocusableElements(container: HTMLElement) {
  return [...container.querySelectorAll<HTMLElement>(focusableSelector)].filter(element => !element.hidden && !element.matches(":disabled") && element.getAttribute("aria-hidden") !== "true");
}

function focusInitialElement(container: HTMLElement) {
  const alreadyFocused = document.activeElement instanceof HTMLElement && container.contains(document.activeElement) && document.activeElement !== container ? document.activeElement : null;
  const autoFocus = [...container.querySelectorAll<HTMLElement>(focusableSelector)].find(element => element.hasAttribute("autofocus") || Boolean((element as HTMLElement & { autofocus?: boolean }).autofocus));
  const focusTarget = alreadyFocused ?? autoFocus ?? getFocusableElements(container)[0] ?? container;
  focusTarget.focus();
}

export function DialogActions({ children }: DialogActionsProps) {
  return <div className="dialog-actions">{children}</div>;
}

/**
 * A modal dialog that preserves the existing application class names while
 * handling focus, keyboard dismissal, and nested-dialog stacking.
 */
export function Dialog({ open, title, onClose, children, wide = false, className = "", closeLabel = "Close" }: DialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  const titleID = useId();
  onCloseRef.current = onClose;

  useLayoutEffect(() => {
    if (!open || !dialogRef.current) return;

    const dialog = dialogRef.current;
    const previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const activeDialog = { element: dialog, order: ++dialogOrder };
    activeDialogs.add(activeDialog);
    focusInitialElement(dialog);

    const handleKeyDown = (event: KeyboardEvent) => {
      if (!isTopDialog(dialog)) return;

      if (event.key === "Escape") {
        event.preventDefault();
        onCloseRef.current();
        return;
      }

      if (event.key !== "Tab") return;

      const focusable = getFocusableElements(dialog);
      if (focusable.length === 0) {
        event.preventDefault();
        dialog.focus();
        return;
      }

      const current = document.activeElement;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && (current === first || !dialog.contains(current))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (current === last || !dialog.contains(current))) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      activeDialogs.delete(activeDialog);
      if (previouslyFocused?.isConnected) previouslyFocused.focus();
    };
  }, [open]);

  const handleBackdropMouseDown = (event: ReactMouseEvent<HTMLDivElement>) => {
    if (event.currentTarget === event.target && isTopDialog(dialogRef.current)) onCloseRef.current();
  };

  if (!open) return null;

  return <div className="dialog-backdrop" role="presentation" onMouseDown={handleBackdropMouseDown}>
    <div ref={dialogRef} className={`dialog ${wide ? "wide" : ""} ${className}`.trim()} role="dialog" aria-modal="true" aria-labelledby={titleID} tabIndex={-1}>
      <div className="dialog-heading"><h2 id={titleID}>{title}</h2><button type="button" className="icon-button" onClick={() => onCloseRef.current()} aria-label={closeLabel} title={closeLabel}><X size={18} /></button></div>
      {children}
    </div>
  </div>;
}
