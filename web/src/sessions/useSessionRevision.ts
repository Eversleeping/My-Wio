import { useCallback,useEffect,useState } from "react";

// Page-level fallback until the shared realtime data layer owns reconnects.
// Native conversation modules use this revision and never start list timers.
export function useSessionRevision() {
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick(value => value + 1), []);
  useEffect(() => {
    const timer = window.setInterval(() => { if (!document.hidden) refresh(); }, 5000);
    return () => window.clearInterval(timer);
  }, [refresh]);
  return { tick, refresh };
}
