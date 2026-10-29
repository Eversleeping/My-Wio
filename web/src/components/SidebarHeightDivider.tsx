import { useRef, useState } from "react";
import { useI18n } from "../i18n";

export const sidebarHeightKey = "wio_codex_sidebar_height";
export const clampSidebarHeight = (value: number) => Math.min(80, Math.max(20, value));
export function readSidebarHeight(storageKey = sidebarHeightKey) {
  try {
    const stored = localStorage.getItem(storageKey);
    const value = stored === null ? 38 : Number(stored);
    return Number.isFinite(value) ? clampSidebarHeight(value) : 38;
  } catch { return 38; }
}

export function SidebarHeightDivider({ value, onChange }: { value: number; onChange: (value: number) => void }) {
  const { t } = useI18n();
  const drag = useRef<{ pointerID: number; y: number; value: number; height: number } | null>(null);
  const [dragging, setDragging] = useState(false);
  const change = (next: number) => onChange(clampSidebarHeight(next));
  return <div className={`sidebar-height-divider${dragging ? " dragging" : ""}`} role="separator" tabIndex={0}
    aria-label={t("codex.resizeSidebarHeight")} aria-orientation="horizontal" aria-valuemin={20} aria-valuemax={80} aria-valuenow={Math.round(value)}
    title={t("codex.resizeSidebarHeight")}
    onPointerDown={event => {
      if (event.button !== 0) return;
      const height = event.currentTarget.parentElement?.getBoundingClientRect().height ?? 0;
      if (!height) return;
      event.preventDefault();
      event.currentTarget.focus();
      event.currentTarget.setPointerCapture(event.pointerId);
      drag.current = { pointerID: event.pointerId, y: event.clientY, value, height };
      setDragging(true);
    }}
    onPointerMove={event => {
      const start = drag.current;
      if (start && start.pointerID === event.pointerId) change(start.value + (event.clientY - start.y) / start.height * 100);
    }}
    onPointerUp={event => {
      if (drag.current?.pointerID !== event.pointerId) return;
      drag.current = null; setDragging(false);
      event.currentTarget.releasePointerCapture(event.pointerId);
    }}
    onPointerCancel={() => { drag.current = null; setDragging(false); }}
    onLostPointerCapture={() => { drag.current = null; setDragging(false); }}
    onDoubleClick={() => change(38)}
    onKeyDown={event => {
      const next = event.key === "ArrowUp" ? value - 2 : event.key === "ArrowDown" ? value + 2 : event.key === "Home" ? 20 : event.key === "End" ? 80 : null;
      if (next !== null) { event.preventDefault(); change(next); }
    }}><span /></div>;
}
