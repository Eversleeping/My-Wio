import { useCallback, useState } from "react";

// Local actions can refresh the page; realtime and disconnection recovery are
// owned by the shared query layer rather than a second page-level timer.
export function useSessionRevision() {
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick(value => value + 1), []);
  return { tick, refresh };
}
