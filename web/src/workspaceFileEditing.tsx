import { useRef, useState } from "react";
import { LoaderCircle } from "lucide-react";
import { api } from "./api";
import { Dialog, DialogActions } from "./components/Dialog";
import { useI18n } from "./i18n";
import type { WorkspaceFilePreview } from "./types";

const maxFileSize = 1024 * 1024;
type Draft = { content: string; hash: string; crlf: boolean; operationID?: string };
const drafts = new Map<string, Draft>();
if (typeof window !== "undefined") window.addEventListener("beforeunload", event => {
  if (drafts.size) { event.preventDefault(); event.returnValue = ""; }
});
export function clearFileDrafts() { drafts.clear(); }
export function hasFileDraft(workspaceID: string, path: string) { return drafts.has(`${workspaceID}:${path}`); }
const errorText = (error: unknown) => error instanceof Error ? error.message : "Request failed";

// Keep the operation id until completion. A timeout/retry must not queue a second write.
async function waitForWrite(id: string) {
  const deadline = Date.now() + 60_000;
  while (true) {
    const operation = await api<{ status: string; result: string }>(`/operations/${encodeURIComponent(id)}`);
    if (operation.status === "succeeded") return;
    if (["failed", "cancelled"].includes(operation.status)) throw new Error(operation.result || "File write failed");
    if (Date.now() >= deadline) throw new Error("File write is still pending. Retry to check its status.");
    await new Promise(resolve => setTimeout(resolve, 750));
  }
}

export function useFileEditor(workspaceID: string, path: string, data: WorkspaceFilePreview | null, writable: boolean, onSaved: () => void, onClose: () => void) {
  const { t } = useI18n();
  const key = `${workspaceID}:${path}`;
  const [draft, setDraft] = useState<Draft | null>(() => drafts.get(key) ?? null);
  const [decision, setDecision] = useState<"" | "finish" | "close">("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const operation = useRef(draft?.operationID ?? "");
  const inFlight = useRef(false);
  const update = (value: Draft | null) => { if (value) drafts.set(key, value); else drafts.delete(key); setDraft(value); };
  const begin = () => {
    if (writable && data?.status === "succeeded" && !data.truncated && data.hash) update({ content: data.content, hash: data.hash, crlf: data.content.includes("\r\n") && !/(?<!\r)\n/.test(data.content) });
  };
  const save = async () => {
    if (!draft || !writable || inFlight.current) return;
    inFlight.current = true; setSaving(true); setError("");
    try {
      const content = draft.crlf ? draft.content.replace(/\r?\n/g, "\r\n") : draft.content;
      if (new TextEncoder().encode(content).length > maxFileSize) throw new Error(t("codex.fileSizeLimit"));
      if (!operation.current) {
        const queued = await api<{ operation_id: string }>(`/workspaces/${workspaceID}/file-content`, { method: "PUT", body: JSON.stringify({ path, content, expected_hash: draft.hash }) });
        operation.current = queued.operation_id;
        update({ ...draft, operationID: queued.operation_id });
      }
      try { await waitForWrite(operation.current); }
      catch (error) {
        // Terminal failures may be retried with a revised draft. Pending operations stay attached.
        const status = await api<{ status: string }>(`/operations/${encodeURIComponent(operation.current)}`).catch(() => null);
        if (status && ["failed", "cancelled"].includes(status.status)) { operation.current = ""; update({ ...draft, operationID: undefined }); }
        throw error;
      }
      operation.current = "";
      update(null); setDecision(""); onSaved();
      if (decision === "close") onClose();
    } catch (error) { setError(errorText(error)); }
    finally { inFlight.current = false; setSaving(false); }
  };
  const close = () => { if (draft) setDecision("close"); else onClose(); };
  const discard = () => { if (saving || operation.current) return; update(null); setError(""); setDecision(""); if (decision === "close") onClose(); };
  return {
    editing: Boolean(draft), saving, error, begin, finish: () => setDecision("finish"), close,
    editor: draft && <textarea className="workspace-file-editor" aria-label={t("codex.editFileContent")} value={draft.content} spellCheck={false} readOnly={!writable || saving || Boolean(operation.current)} onChange={event => update({ ...draft, content: event.target.value })} />,
    dialog: <Dialog open={Boolean(decision)} title={t("codex.finishEditing")} closeLabel={t("common.close")} dismissDisabled={saving} onClose={() => setDecision("")}>
      <div><p>{t("codex.saveFileQuestion", { path })}</p>
      {error && <div className="error-banner" role="alert">{error}</div>}
      <DialogActions><button className="secondary-button" disabled={saving} onClick={() => setDecision("")}>{t("codex.continueEditing")}</button><button className="secondary-button danger" disabled={saving || Boolean(operation.current)} onClick={discard}>{t("codex.discardFileEdit")}</button><button className="primary-button" disabled={saving || !writable} onClick={() => void save()}>{saving && <LoaderCircle className="spin" size={15} />}{t("common.save")}</button></DialogActions>
    </div></Dialog>
  };
}

export function AddWorkspaceFileDialog({ workspaceID, onClose, onCreated }: { workspaceID: string; onClose: () => void; onCreated: (path: string) => void }) {
  const { t } = useI18n();
  const [path, setPath] = useState("");
  const [content, setContent] = useState("");
  const [upload, setUpload] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const operation = useRef("");
  const inFlight = useRef(false);
  const create = async () => {
    if (!path.trim() || inFlight.current) return;
    inFlight.current = true; setBusy(true); setError("");
    try {
      let payload: Record<string, string> = { path: path.trim(), content };
      if (upload) {
        if (upload.size > maxFileSize) throw new Error(t("codex.fileSizeLimit"));
        const bytes = new Uint8Array(await upload.arrayBuffer());
        let binary = "";
        for (const byte of bytes) binary += String.fromCharCode(byte);
        payload = { path: path.trim(), content_base64: btoa(binary) };
      } else if (new TextEncoder().encode(content).length > maxFileSize) throw new Error(t("codex.fileSizeLimit"));
      if (!operation.current) {
        const result = await api<{ operation_id: string }>(`/workspaces/${workspaceID}/files`, { method: "POST", body: JSON.stringify(payload) });
        operation.current = result.operation_id;
      }
      try { await waitForWrite(operation.current); }
      catch (error) {
        const status = await api<{ status: string }>(`/operations/${encodeURIComponent(operation.current)}`).catch(() => null);
        if (status && ["failed", "cancelled"].includes(status.status)) operation.current = "";
        throw error;
      }
      operation.current = "";
      onCreated(path.trim()); onClose();
    } catch (error) { setError(errorText(error)); }
    finally { inFlight.current = false; setBusy(false); }
  };
  return <Dialog open title={t("codex.addFile")} wide closeLabel={t("common.close")} dismissDisabled={busy || Boolean(operation.current)} onClose={onClose}>
    <form className="workspace-add-file" onSubmit={event => { event.preventDefault(); void create(); }}>
      <label>{t("codex.newFilePath")}<input autoFocus value={path} maxLength={1024} placeholder="src/example.ts" disabled={busy || Boolean(operation.current)} onChange={event => setPath(event.target.value)} required /></label>
      <label>{t("codex.uploadFile")}<input type="file" disabled={busy || Boolean(operation.current)} onChange={event => { const file = event.target.files?.[0] ?? null; setUpload(file); if (file && !path) setPath(file.name); }} /></label>
      {!upload && <label>{t("codex.editFileContent")}<textarea rows={10} value={content} spellCheck={false} disabled={busy || Boolean(operation.current)} onChange={event => setContent(event.target.value)} /></label>}
      <p className="muted">{t("codex.addFileHint")}</p>
      {error && <div className="error-banner" role="alert">{error}</div>}
      <DialogActions><button type="button" className="secondary-button" disabled={busy || Boolean(operation.current)} onClick={onClose}>{t("common.cancel")}</button><button className="primary-button" disabled={busy || !path.trim()}>{busy && <LoaderCircle className="spin" size={15} />}{t("codex.addFile")}</button></DialogActions>
    </form>
  </Dialog>;
}
