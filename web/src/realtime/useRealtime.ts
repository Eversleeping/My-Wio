import { useCallback, useEffect, useRef, useState } from "react";
import { api, APIError, socketURL } from "../api";
import type { StreamEvent } from "../types";
import { allRealtimeScopes, initialRealtimeRevisions, realtimeScopesForEvent, type RealtimeScope, type RealtimeRevisions } from "./scopes";

type StreamRevision = { revision: number; minimumSequence: number | null };

export type StreamRevisions = Record<string, StreamRevision>;

const websocketReconnectBaseDelay = 1_000;

const websocketReconnectMaximumDelay = 30_000;

export const websocketStableConnectionDelay = 30_000;

/** Exponential reconnect delay with equal jitter: 0.5–1 s first, doubling up to 15–30 s. */
export function websocketReconnectDelay(attempt: number, random = Math.random) {
  const ceiling = Math.min(websocketReconnectMaximumDelay, websocketReconnectBaseDelay * 2 ** Math.max(0, attempt));
  return Math.round(ceiling / 2 + random() * ceiling / 2);
}

export type StreamInvalidationListener = (streamID: string, minimumSequence: number | null) => void;

export function useRealtime(authenticated: boolean) {
  const [realtimeRevisions, setRealtimeRevisions] = useState<RealtimeRevisions>(initialRealtimeRevisions);
  const [streamRevisions, setStreamRevisions] = useState<StreamRevisions>({});
  const [socketConnected, setSocketConnected] = useState(false);
  const streamListeners = useRef(new Set<StreamInvalidationListener>());
  const subscribe = useCallback((listener: StreamInvalidationListener) => {
    streamListeners.current.add(listener);
    return () => { streamListeners.current.delete(listener); };
  }, []);

  useEffect(() => {
    if (!authenticated) return;
    let socket: WebSocket | null = null;
    let timer = 0;
    let refreshTimer = 0;
    let stableTimer = 0;
    let reconnectAttempt = 0;
    let stopped = false;
    const pendingStreams = new Map<string, number | null>();
    const pendingRealtimeScopes = new Set<RealtimeScope>();
    const markPendingStream = (streamID: string, sequence?: number) => {
      const nextSequence = typeof sequence === "number" && Number.isInteger(sequence) && sequence > 0 ? sequence : null;
      const current = pendingStreams.get(streamID);
      if (current === undefined) pendingStreams.set(streamID, nextSequence);
      else if (current === null || nextSequence === null) pendingStreams.set(streamID, null);
      else pendingStreams.set(streamID, Math.min(current, nextSequence));
    };
    const markPendingRealtimeScopes = (scopes: RealtimeScope[]) => {
      for (const scope of scopes) pendingRealtimeScopes.add(scope);
    };
    const scheduleRefresh = () => {
      if (refreshTimer) return;
      refreshTimer = window.setTimeout(() => {
        refreshTimer = 0;
        if (pendingRealtimeScopes.size > 0) {
          const scopes = Array.from(pendingRealtimeScopes);
          pendingRealtimeScopes.clear();
          setRealtimeRevisions(current => {
            const next = { ...current };
            for (const scope of scopes) next[scope] += 1;
            return next;
          });
        }
        if (pendingStreams.size > 0) {
          const streams = Array.from(pendingStreams.entries());
          pendingStreams.clear();
          // Cached views keep the lowest sequence of every batch, including
          // batches that arrive while another thread is shown.
          for (const [streamID, minimumSequence] of streams) for (const listener of streamListeners.current) listener(streamID, minimumSequence);
          setStreamRevisions(current => {
            const next = { ...current };
            for (const [streamID, minimumSequence] of streams) next[streamID] = { revision: (next[streamID]?.revision ?? 0) + 1, minimumSequence };
            return next;
          });
        }
      }, 100);
    };
    const reconnect = () => {
      if (stopped) return;
      timer = window.setTimeout(connect, websocketReconnectDelay(reconnectAttempt));
      reconnectAttempt += 1;
    };
    const connect = () => {
      if (stopped) return;
      let opened = false;
      socket = new WebSocket(socketURL());
      socket.onopen = () => {
        opened = true;
        // Only a connection that stays up restores the short initial retry delay.
        stableTimer = window.setTimeout(() => { reconnectAttempt = 0; }, websocketStableConnectionDelay);
        setSocketConnected(true); markPendingStream("*"); markPendingRealtimeScopes(allRealtimeScopes); scheduleRefresh();
      };
      socket.onmessage = messageEvent => {
        try {
          const event = JSON.parse(String(messageEvent.data)) as Partial<StreamEvent>;
          markPendingStream(event.stream_id || "*", event.sequence);
          markPendingRealtimeScopes(realtimeScopesForEvent(event));
        } catch {
          markPendingStream("*");
          markPendingRealtimeScopes(allRealtimeScopes);
        }
        scheduleRefresh();
      };
      socket.onclose = () => {
        clearTimeout(stableTimer);
        setSocketConnected(false);
        if (stopped) return;
        if (opened) { reconnect(); return; }
        // Browsers hide the status of a rejected upgrade. Check the session first:
        // a 401 signs out through the central handler instead of retrying forever.
        api("/auth/session").then(reconnect, error => { if (!(error instanceof APIError && error.status === 401)) reconnect(); });
      };
    };
    connect();
    return () => {
      stopped = true;
      clearTimeout(timer);
      clearTimeout(refreshTimer);
      clearTimeout(stableTimer);
      socket?.close();
    };
  }, [authenticated]);

  return { realtimeRevisions, streamRevisions, socketConnected, subscribe };
}
