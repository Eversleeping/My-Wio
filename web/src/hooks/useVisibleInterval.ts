import { useEffect, useRef, useState } from "react";

// Pause clocks and exceptional polling while hidden; catch up on return.
export function useVisibleInterval(callback: () => void, delay: number | null) {
  const current = useRef(callback);
  current.current = callback;
  useEffect(() => {
    if (delay === null) return;
    let timer: number | undefined;
    const stop = () => { window.clearInterval(timer); timer = undefined; };
    const start = () => { if (!document.hidden) timer = window.setInterval(() => current.current(), delay); };
    const visibility = () => { stop(); if (!document.hidden) { current.current(); start(); } };
    start();
    document.addEventListener("visibilitychange", visibility);
    return () => { stop(); document.removeEventListener("visibilitychange", visibility); };
  }, [delay]);
}

export function useVisibleClock(active: boolean, delay: number) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => { if (active && !document.hidden) setNow(Date.now()); }, [active]);
  useVisibleInterval(() => setNow(Date.now()), active ? delay : null);
  return now;
}
