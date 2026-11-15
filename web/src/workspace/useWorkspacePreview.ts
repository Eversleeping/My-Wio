import { useState, useRef, useCallback, useEffect } from "react";
import { api } from "../api";
import { message } from "../text";

const previewPollDelays = [750, 1_500, 3_000, 6_000];

const previewPollTimeout = 20_000;

type WorkspacePreviewSnapshot = { path: string; status: string; error: string };

export function useWorkspacePreview<T extends WorkspacePreviewSnapshot>(workspaceID: string, path: string, kind: "file-preview" | "diff-preview", realtime: number, timeoutMessage: string, enabled = true) {
  const key = `${workspaceID}:${kind}:${path}`;
  const endpoint = `/workspaces/${workspaceID}/${kind}?path=${encodeURIComponent(path)}`;
  const [requestNonce, setRequestNonce] = useState(0);
  const [state, setState] = useState<{ key: string; data: T | null; error: string; loading: boolean; requesting: boolean }>({ key: "", data: null, error: "", loading: false, requesting: false });
  const dataRef = useRef<T | null>(null);
  const confirmRef = useRef<(() => void) | null>(null);
  const lastRealtimeRef = useRef(realtime);
  const retry = useCallback(() => setRequestNonce(value => value + 1), []);

  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    let active = true;
    let started = false;
    let checking = false;
    let waitingForTerminalState = true;
    let pollAttempt = 0;
    let timer = 0;
    let startedAt = 0;
    let hiddenAt = document.hidden ? Date.now() : 0;
    let hiddenDuration = 0;
    const clearTimer = () => { if (timer) { window.clearTimeout(timer); timer = 0; } };
    const visibleElapsed = () => startedAt ? Date.now() - startedAt - hiddenDuration - (hiddenAt ? Date.now() - hiddenAt : 0) : 0;
    const fail = (error: string) => {
      if (!active) return;
      waitingForTerminalState = false;
      clearTimer();
      setState(current => current.key === key ? { ...current, error, loading: false, requesting: false } : current);
    };
    const scheduleConfirmation = () => {
      if (!active || !waitingForTerminalState || document.hidden) return;
      const remaining = previewPollTimeout - visibleElapsed();
      if (remaining <= 0) { fail(timeoutMessage); return; }
      const delay = previewPollDelays[Math.min(pollAttempt, previewPollDelays.length - 1)];
      pollAttempt += 1;
      timer = window.setTimeout(() => {
        timer = 0;
        if (!document.hidden) void confirm();
      }, Math.min(delay, remaining));
    };
    const confirm = async () => {
      if (!active || !waitingForTerminalState || checking || document.hidden) return;
      checking = true;
      try {
        const snapshot = await api<T>(endpoint, { signal: controller.signal });
        if (!active) return;
        const terminal = snapshot.status === "succeeded" || snapshot.status === "failed";
        if (terminal) {
          waitingForTerminalState = false;
          clearTimer();
        }
        if (snapshot.status === "succeeded") dataRef.current = snapshot;
        const displayData = snapshot.status === "succeeded" ? snapshot : dataRef.current ?? snapshot;
        setState({ key, data: displayData, error: snapshot.status === "failed" ? snapshot.error : "", loading: !terminal, requesting: false });
        if (!terminal) scheduleConfirmation();
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        fail(message(error));
      } finally {
        checking = false;
      }
    };
    const start = async () => {
      if (!active || started || document.hidden) return;
      started = true;
      if (dataRef.current?.path !== path) dataRef.current = state.key === key ? state.data : null;
      startedAt = Date.now();
      setState(current => current.key === key ? { ...current, error: "", loading: true, requesting: true } : { key, data: null, error: "", loading: true, requesting: true });
      try {
        await api(`/workspaces/${workspaceID}/${kind}`, { method: "POST", body: JSON.stringify({ path }), signal: controller.signal });
        if (!active) return;
        setState(current => current.key === key ? { ...current, requesting: false } : current);
        await confirm();
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        fail(message(error));
      }
    };
    const confirmCurrent = () => { void confirm(); };
    confirmRef.current = confirmCurrent;
    const visibilityChanged = () => {
      if (document.hidden) {
        if (!hiddenAt) hiddenAt = Date.now();
        clearTimer();
        return;
      }
      if (hiddenAt) { hiddenDuration += Date.now() - hiddenAt; hiddenAt = 0; }
      if (!started) void start();
      else void confirm();
    };
    document.addEventListener("visibilitychange", visibilityChanged);
    if (!document.hidden) void start();
    else setState(current => current.key === key ? { ...current, error: "", loading: true, requesting: false } : { key, data: null, error: "", loading: true, requesting: false });
    return () => {
      active = false;
      clearTimer();
      controller.abort();
      document.removeEventListener("visibilitychange", visibilityChanged);
      if (confirmRef.current === confirmCurrent) confirmRef.current = null;
    };
  }, [enabled, endpoint, key, kind, path, requestNonce, timeoutMessage, workspaceID]);

  useEffect(() => {
    if (lastRealtimeRef.current === realtime) return;
    lastRealtimeRef.current = realtime;
    confirmRef.current?.();
  }, [realtime]);

  const current = state.key === key ? state : { key, data: null, error: "", loading: true, requesting: false };
  return { ...current, retry };
}
