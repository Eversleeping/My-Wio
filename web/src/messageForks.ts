import type { StreamEvent } from "./types";

function payload(event: StreamEvent): { turnId?: string; item?: { id?: string; type?: string; phase?: string }; turn?: { id?: string; status?: string } } {
  return event.payload && typeof event.payload === "object" ? event.payload : {};
}

// Offer branching only on the final assistant reply of a persisted, finished
// turn. Commentary and partial replies cannot define a turn boundary.
export function messageForkTargets(source: StreamEvent[], visible: StreamEvent[]): Set<string> {
  const terminal = new Map<string, number>();
  for (const event of source) {
    const turn = payload(event).turn;
    if (event.kind === "codex.turn.completed" && turn?.id && ["completed", "failed", "interrupted"].includes(turn.status ?? "")) terminal.set(turn.id, event.sequence);
  }
  const replies = new Map<string, StreamEvent>();
  for (const event of visible) {
    const { turnId, item } = payload(event);
    if (event.kind === "codex.item.completed" && turnId && item?.type === "agentMessage") replies.set(turnId, event);
  }
  return new Set([...replies].filter(([id, event]) => payload(event).item?.id && payload(event).item?.phase !== "commentary" && (terminal.get(id) ?? 0) > event.sequence).map(([, event]) => event.event_id));
}
