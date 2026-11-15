import { createContext, useContext, useEffect, useRef, type ReactNode } from "react";
import { useVisibleInterval } from "../hooks/useVisibleInterval";

type Reader = () => void;
const Readers = createContext<Set<Reader> | null>(null);

// One timer per application, shared by active queries and native event windows.
export function RealtimeFallback({ connected, children }: {connected: boolean; children: ReactNode}) {
  const readers = useRef(new Set<Reader>());
  const previous = useRef(connected);
  const refresh = () => { for (const reader of readers.current) reader(); };
  useVisibleInterval(refresh, connected ? null : 5000);
  useEffect(() => {
    if (connected && !previous.current && !document.hidden) refresh();
    previous.current = connected;
  }, [connected]);
  return <Readers.Provider value={readers.current}>{children}</Readers.Provider>;
}

export function useFallbackRefresh(refresh: Reader, active = true) {
  const readers = useContext(Readers);
  const current = useRef(refresh);
  current.current = refresh;
  useEffect(() => {
    if (!readers || !active) return;
    const reader = () => current.current();
    readers.add(reader);
    return () => { readers.delete(reader); };
  }, [active, readers]);
}
