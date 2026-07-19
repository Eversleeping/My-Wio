import { KeyboardEvent as ReactKeyboardEvent, ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { MoreVertical, type LucideIcon } from "lucide-react";

export interface ContextMenuAction {
  id: string;
  label: string;
  icon: LucideIcon;
  onSelect: () => void | Promise<void>;
  disabled?: boolean;
  danger?: boolean;
  separatorBefore?: boolean;
}

export function ContextMenu({ className = "", label, actions, children }: { className?: string; label: string; actions: ContextMenuAction[]; children: ReactNode }) {
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ left: 0, top: 0 });

  const close = useCallback((restoreFocus = true) => {
    setOpen(false);
    if (restoreFocus) requestAnimationFrame(() => triggerRef.current?.focus());
  }, []);

  const openAt = useCallback((left: number, top: number) => {
    setPosition({ left, top });
    setOpen(true);
  }, []);

  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => {
      const menu = menuRef.current;
      if (!menu) return;
      const rect = menu.getBoundingClientRect();
      const gutter = 8;
      setPosition(current => ({
        left: Math.max(gutter, Math.min(current.left, window.innerWidth - rect.width - gutter)),
        top: Math.max(gutter, Math.min(current.top, window.innerHeight - rect.height - gutter))
      }));
      menu.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus();
    });
    const onPointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node) && !menuRef.current?.contains(event.target as Node)) close(false);
    };
    const onResize = () => close(false);
    document.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("resize", onResize);
    window.addEventListener("scroll", onResize, true);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("resize", onResize);
      window.removeEventListener("scroll", onResize, true);
    };
  }, [close, open]);

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const buttons = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []);
    if (event.key === "Escape") {
      event.preventDefault();
      close();
      return;
    }
    if (event.key === "Tab") {
      close(false);
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" && event.key !== "Home" && event.key !== "End") return;
    event.preventDefault();
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : event.key === "ArrowDown" ? (current + 1) % buttons.length : (current - 1 + buttons.length) % buttons.length;
    buttons[next]?.focus();
  };

  return <div ref={rootRef} className={`context-menu-trigger ${className}`} onContextMenu={event => { event.preventDefault(); const rect = rootRef.current?.getBoundingClientRect(); const left = event.clientX || rect?.left || 0; const top = event.clientY || rect?.bottom || 0; openAt(left, top); }}>
    {children}
    <button ref={triggerRef} type="button" className="icon-button context-menu-button" aria-haspopup="menu" aria-expanded={open} title={label} aria-label={label} onClick={event => { event.stopPropagation(); const rect = event.currentTarget.getBoundingClientRect(); openAt(rect.right, rect.bottom + 4); }}><MoreVertical size={16} /></button>
    {open && <div ref={menuRef} className="context-menu" role="menu" aria-label={label} style={position} onKeyDown={onKeyDown}>
      {actions.map(action => { const Icon = action.icon; return <div className={action.separatorBefore ? "context-menu-section" : undefined} key={action.id}><button type="button" role="menuitem" className={action.danger ? "danger" : ""} disabled={action.disabled} onClick={() => { close(false); void action.onSelect(); }}><Icon size={16} /><span>{action.label}</span></button></div>; })}
    </div>}
  </div>;
}
