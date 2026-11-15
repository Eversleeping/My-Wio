import { lazy, useEffect, useState, Suspense, useRef } from "react";
import { type FilePreviewSelection } from "../codexContent";
import { useI18n } from "../i18n";
import { useWorkspacePreview } from "../workspace/useWorkspacePreview";
import { type WorkspaceFilePreview, type WorkspaceDiffPreview } from "../types";
import { useFileEditor } from "../workspaceFileEditing";
import { formatDate } from "../format";
import { FileCode2, Eye, Code2, Pencil, LoaderCircle, RefreshCw, X, AlertTriangle, FileDiff, Undo2 } from "lucide-react";
import { ImageViewer } from "./ImageViewer";
import { previewImageSource } from "../imageContent";
import { MarkdownContentRenderer } from "./LazyMarkdown";
import { post } from "../api";
import { message } from "../text";
import { ConfirmDialog } from "./ConfirmDialog";

const HighlightedFile = lazy(() => import("../FilePreviewCode"));

const HighlightedDiff = lazy(() => import("../FileDiffCode"));

type FilePreviewPaneProps = { workspaceID: string; selection: FilePreviewSelection; realtime: number; writable?: boolean; workspaceRoot?: string; enabled?: boolean; closeRequest?: number; onOpenFile?: (selection: FilePreviewSelection) => void; onClose: () => void };

export function FilePreviewPane(props: FilePreviewPaneProps) { return <EditableFilePreviewPane key={`${props.workspaceID}:${props.selection.path}`} {...props} />; }

function EditableFilePreviewPane({ workspaceID, selection, realtime, writable = false, workspaceRoot = "", enabled = true, closeRequest = 0, onOpenFile, onClose }: FilePreviewPaneProps) {
  const { t } = useI18n();
  const preview = useWorkspacePreview<WorkspaceFilePreview>(workspaceID, selection.path, "file-preview", realtime, t("codex.previewTimedOut"), enabled);
  const edit = useFileEditor(workspaceID, selection.path, preview.data, writable, preview.retry, onClose);
  useEffect(() => { if (closeRequest) edit.close(); }, [closeRequest]);
  const [showSource, setShowSource] = useState(Boolean(selection.line));
  useEffect(() => { setShowSource(Boolean(selection.line)); }, [selection.line]);
  const data = preview.data;
  const loading = preview.loading || !data || data.status === "idle" || data.status === "loading";
  const rawError = preview.error || data?.error || "";
  const error = rawError === "image preview exceeds the 4 MB limit" ? t("codex.imagePreviewTooLarge") : rawError;
  const showData = data?.status === "succeeded";
  const updatedText = data?.updated_at ? t("codex.previewUpdated", { time: formatDate(data.updated_at) }) : "";
  const language = previewLanguage(selection.path);
  const fileName = selection.path.split("/").pop() || selection.path;
  const image = Boolean(data?.encoding);
  return <><section className={`file-preview-panel ${edit.editing ? "editing" : ""} ${image ? "image-preview-panel" : ""}`}>
    <header className="file-preview-header"><div><FileCode2 size={17} /><span><h2>{fileName}</h2><small title={selection.path}>{selection.path}</small></span></div><div className="file-preview-actions">{showData && <><span className="file-language">{image ? data.mime_type?.replace("image/", "").toUpperCase() : language.label}</span><span className="file-size">{formatFileSize(data.size)}</span></>}{!image && language.id === "markdown" && !edit.editing && <button type="button" className={`icon-button ${showSource ? "active" : ""}`} aria-pressed={showSource} title={t(showSource ? "codex.viewMarkdown" : "codex.viewSource")} aria-label={t(showSource ? "codex.viewMarkdown" : "codex.viewSource")} onClick={() => setShowSource(value => !value)}>{showSource ? <Eye size={15} /> : <Code2 size={15} />}</button>}<button type="button" className={`icon-button ${edit.editing ? "active" : ""}`} aria-pressed={edit.editing} disabled={edit.saving || (!edit.editing && (!writable || !showData || image || data.truncated || !data.hash || preview.loading))} title={t(edit.editing ? "codex.finishEditing" : "codex.editFile")} aria-label={t(edit.editing ? "codex.finishEditing" : "codex.editFile")} onClick={edit.editing ? edit.finish : edit.begin}><Pencil size={15} /></button><button type="button" className="icon-button" disabled={preview.requesting || edit.editing} title={t("codex.refreshPreview")} aria-label={t("codex.refreshPreview")} onClick={preview.retry}>{preview.requesting ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button><button type="button" className="icon-button" title={t("codex.closePreview")} aria-label={t("codex.closePreview")} disabled={edit.saving} onClick={edit.close}><X size={16} /></button></div></header>
    <div className="file-preview-body">
      {showData && (error || preview.loading) && <PreviewStatusNote messageText={error ? t("codex.previewStale", { error }) : t("codex.previewRefreshing")} updatedText={updatedText} loading={preview.loading} retry={preview.retry} retryLabel={t("common.retry")} />}
      {edit.editing ? edit.editor : error && !showData ? <div className="file-preview-error"><AlertTriangle size={22} /><strong>{t("codex.previewFailed")}</strong><span>{error}</span><button type="button" className="secondary-button" onClick={preview.retry}>{t("common.retry")}</button></div> : !showData && loading ? <div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingPreview")}</span></div> : showData && image ? <ImageViewer src={previewImageSource(data)} alt={fileName} /> : showData ? <>{data.truncated && <div className="file-preview-note"><AlertTriangle size={14} />{t("codex.previewTruncated", { size: formatFileSize(data.size) })}</div>}<Suspense fallback={<div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingPreview")}</span></div>}>{language.id === "markdown" && !showSource ? <div className="file-markdown"><MarkdownContentRenderer text={data.content} workspaceRoot={workspaceRoot} filePath={selection.path} onOpenFile={onOpenFile} /></div> : <HighlightedFile content={data.content} language={language.id} targetLine={selection.line} />}</Suspense></> : null}
    </div>
  </section>{edit.dialog}</>;
}

export function FileDiffPane({ workspaceID, selection, realtime, writable, notify, onOpenFile, onClose, enabled = true }: { workspaceID: string; selection: FilePreviewSelection; realtime: number; writable: boolean; notify: (text: string) => void; onOpenFile?: (selection: FilePreviewSelection) => void; onClose: () => void; enabled?: boolean }) {
  const { t } = useI18n();
  const [discarding, setDiscarding] = useState(false);
  const [discardConfirmation, setDiscardConfirmation] = useState(false);
  const previewBodyRef = useRef<HTMLDivElement>(null);
  const preview = useWorkspacePreview<WorkspaceDiffPreview>(workspaceID, selection.path, "diff-preview", realtime, t("codex.diffTimedOut"), enabled);
  const data = preview.data;
  const loading = preview.loading || !data || data.status === "idle" || data.status === "loading";
  const error = preview.error || data?.error || "";
  const showData = data?.status === "succeeded";
  const updatedText = data?.updated_at ? t("codex.diffUpdated", { time: formatDate(data.updated_at) }) : "";
  const language = previewLanguage(selection.path);
  const fileName = selection.path.split("/").pop() || selection.path;
  const retry = preview.retry;
  const discard = async () => {
    if (!writable || discarding) return;
    setDiscarding(true);
    try {
      await post(`/workspaces/${workspaceID}/git/discard`, { paths: [selection.path], all: false, include_staged: true });
      notify(t("codex.discardFileQueued"));
      setDiscardConfirmation(false);
      onClose();
    } catch (error) {
      notify(message(error));
    } finally {
      setDiscarding(false);
    }
  };
  return <><section className="file-preview-panel file-diff-panel">
    <header className="file-preview-header"><div><FileDiff size={17} /><span><h2>{fileName}</h2><small title={selection.path}>{selection.path}</small></span></div><div className="file-preview-actions">{showData && <><span className="diff-stat additions">+{data.additions}</span><span className="diff-stat deletions">-{data.deletions}</span></>}{language.id === "markdown" && onOpenFile && <button type="button" className="icon-button" title={t("codex.viewMarkdown")} aria-label={t("codex.viewMarkdown")} onClick={() => onOpenFile({ path: selection.path, mode: "file" })}><Eye size={15} /></button>}<button type="button" className="icon-button danger" disabled={!writable || discarding} title={t(writable ? "codex.discardFile" : "project.gitReadOnly")} aria-label={t("codex.discardFile")} onClick={() => setDiscardConfirmation(true)}>{discarding ? <LoaderCircle className="spin" size={15} /> : <Undo2 size={15} />}</button><button type="button" className="icon-button" disabled={preview.requesting || discarding} title={t("codex.refreshDiff")} aria-label={t("codex.refreshDiff")} onClick={retry}>{preview.requesting ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button><button type="button" className="icon-button" title={t("codex.closeDiff")} aria-label={t("codex.closeDiff")} onClick={onClose}><X size={16} /></button></div></header>
    <div className="file-preview-body" ref={previewBodyRef}>
      {showData && (error || preview.loading) && <PreviewStatusNote messageText={error ? t("codex.diffStale", { error }) : t("codex.diffRefreshing")} updatedText={updatedText} loading={preview.loading} retry={retry} retryLabel={t("common.retry")} />}
      {error && !showData ? <div className="file-preview-error"><AlertTriangle size={22} /><strong>{t("codex.diffFailed")}</strong><span>{error}</span><button type="button" className="secondary-button" onClick={retry}>{t("common.retry")}</button></div> : !showData && loading ? <div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingDiff")}</span></div> : showData ? <>{data.truncated && <div className="file-preview-note"><AlertTriangle size={14} />{t("codex.diffTruncated")}</div>}{data.binary ? <div className="file-preview-empty"><FileDiff size={24} /><span>{t("codex.binaryDiff")}</span></div> : !data.content ? <div className="file-preview-empty"><FileDiff size={24} /><span>{t("codex.noTextDiff")}</span></div> : <Suspense fallback={<div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingDiff")}</span></div>}><HighlightedDiff content={data.content} language={language.id} unchangedLabel={count => t("codex.unchangedLines", { count })} scrollRef={previewBodyRef} /></Suspense>}</> : null}
    </div>
  </section><ConfirmDialog open={discardConfirmation} danger title={t("codex.discardFile")} impact={t("codex.discardFileConfirm", { path: selection.path })} confirmLabel={t("codex.discardFile")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={discarding} onClose={() => setDiscardConfirmation(false)} onConfirm={discard} /></>;
}

function previewLanguage(path: string) {
  const fileName = path.split("/").pop()?.toLowerCase() ?? "";
  const extension = fileName.includes(".") ? fileName.split(".").pop() ?? "" : "";
  const special: Record<string, { id: string; label: string }> = {
    dockerfile: { id: "docker", label: "Dockerfile" }, makefile: { id: "makefile", label: "Makefile" },
    ".env": { id: "bash", label: "Environment" }, "go.mod": { id: "go", label: "Go module" }, "go.sum": { id: "plain", label: "Go checksum" }
  };
  if (special[fileName]) return special[fileName];
  const languages: Record<string, { id: string; label: string }> = {
    js: { id: "javascript", label: "JavaScript" }, jsx: { id: "jsx", label: "JSX" }, ts: { id: "typescript", label: "TypeScript" }, tsx: { id: "tsx", label: "TSX" },
    css: { id: "css", label: "CSS" }, scss: { id: "css", label: "SCSS" }, html: { id: "markup", label: "HTML" }, xml: { id: "markup", label: "XML" }, svg: { id: "markup", label: "SVG" },
    go: { id: "go", label: "Go" }, py: { id: "python", label: "Python" }, java: { id: "java", label: "Java" }, c: { id: "c", label: "C" }, h: { id: "c", label: "C header" }, cpp: { id: "cpp", label: "C++" }, cc: { id: "cpp", label: "C++" }, rs: { id: "rust", label: "Rust" }, swift: { id: "swift", label: "Swift" },
    sh: { id: "bash", label: "Shell" }, bash: { id: "bash", label: "Bash" }, ps1: { id: "powershell", label: "PowerShell" }, sql: { id: "sql", label: "SQL" },
    json: { id: "json", label: "JSON" }, jsonc: { id: "json", label: "JSON" }, yaml: { id: "yaml", label: "YAML" }, yml: { id: "yaml", label: "YAML" }, toml: { id: "toml", label: "TOML" }, ini: { id: "plain", label: "INI" },
    md: { id: "markdown", label: "Markdown" }, mdx: { id: "markdown", label: "MDX" }, txt: { id: "plain", label: "Text" }
  };
  return languages[extension] ?? { id: "plain", label: extension ? extension.toUpperCase() : "Text" };
}

function formatFileSize(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(size < 10 * 1024 ? 1 : 0)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function PreviewStatusNote({ messageText, updatedText, loading, retry, retryLabel }: { messageText: string; updatedText: string; loading: boolean; retry: () => void; retryLabel: string }) {
  return <div className={`file-preview-note preview-status-note ${loading ? "loading" : "warning"}`} role="status"><span className="preview-status-message">{loading ? <LoaderCircle className="spin" size={14} /> : <AlertTriangle size={14} />}<strong>{messageText}</strong></span>{updatedText && <small>{updatedText}</small>}<button type="button" className="text-button" disabled={loading} onClick={retry}>{retryLabel}</button></div>;
}
