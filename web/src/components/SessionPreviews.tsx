import { useEffect, useRef, useState, type ReactNode } from "react";
import { FileCode2, FileDiff, MessageSquare, X } from "lucide-react";
import { FileDiffPane, FilePreviewPane } from "../App";
import type { FilePreviewSelection } from "../codexContent";
import { useI18n } from "../i18n";
import { hasFileDraft } from "../workspaceFileEditing";

type PreviewTab = { key: string; selection: FilePreviewSelection; closeRequest: number };
type PreviewState = { scope: string; tabs: PreviewTab[]; active: string | null };
const previewKey = (selection: FilePreviewSelection) => JSON.stringify([selection.mode ?? "file", selection.path]);
const empty = (scope: string): PreviewState => ({ scope, tabs: [], active: null });

export function useSessionPreviews(scope: string, workspaceID: string) {
  const [state, setState] = useState<PreviewState>(() => empty(scope));
  const current = state.scope === scope ? state : empty(scope);
  useEffect(() => { setState(previous => previous.scope === scope ? previous : empty(scope)); }, [scope]);
  const open = (selection: FilePreviewSelection) => setState(previous => {
    const value = previous.scope === scope ? previous : empty(scope);
    const key = previewKey(selection);
    const existing = value.tabs.some(tab => tab.key === key);
    return { ...value, active: key, tabs: existing ? value.tabs.map(tab => tab.key === key ? { ...tab, selection } : tab) : [...value.tabs, { key, selection, closeRequest: 0 }] };
  });
  const select = (key: string | null) => setState(previous => previous.scope === scope ? { ...previous, active: key } : empty(scope));
  const remove = (key: string) => setState(previous => {
    if (previous.scope !== scope) return previous;
    const index = previous.tabs.findIndex(tab => tab.key === key);
    if (index < 0) return previous;
    const tabs = previous.tabs.filter(tab => tab.key !== key);
    return { ...previous, tabs, active: previous.active === key ? (tabs[Math.min(index, tabs.length - 1)]?.key ?? null) : previous.active };
  });
  const requestClose = (tab: PreviewTab) => {
    if (tab.selection.mode !== "diff" && hasFileDraft(workspaceID, tab.selection.path)) {
      setState(previous => previous.scope !== scope ? previous : { ...previous, active: tab.key, tabs: previous.tabs.map(item => item.key === tab.key ? { ...item, closeRequest: item.closeRequest + 1 } : item) });
    } else remove(tab.key);
  };
  return { ...current, preview: current.tabs.find(tab => tab.key === current.active)?.selection ?? null, open, select, remove, requestClose };
}

type Previews = ReturnType<typeof useSessionPreviews>;
export function SessionPreviewTabs({ previews }: { previews: Previews }) {
  const { t } = useI18n();
  const activeTab = useRef<HTMLButtonElement>(null);
  useEffect(() => { activeTab.current?.scrollIntoView?.({ block: "nearest", inline: "nearest" }); }, [previews.active, previews.tabs.length]);
  if (!previews.tabs.length) return null;
  return <div className="session-pane-tabs" role="tablist" aria-label={t("codex.sessionViews")}>
    <button type="button" role="tab" ref={previews.active === null ? activeTab : undefined} aria-selected={previews.active === null} className={previews.active === null ? "active" : ""} onClick={() => previews.select(null)}><MessageSquare size={15} />{t("codex.conversation")}</button>
    {previews.tabs.map(tab => <div className={`session-file-tab ${previews.active === tab.key ? "active" : ""}`} key={tab.key}>
      <button type="button" role="tab" ref={previews.active === tab.key ? activeTab : undefined} aria-selected={previews.active === tab.key} title={tab.selection.path} onClick={() => previews.select(tab.key)}>{tab.selection.mode === "diff" ? <FileDiff size={15} /> : <FileCode2 size={15} />}<span>{tab.selection.path.split(/[\\/]/).pop() || tab.selection.path}</span>{tab.selection.mode === "diff" && <small>{t("codex.diffTab")}</small>}</button>
      <button type="button" className="session-tab-close" aria-label={t(tab.selection.mode === "diff" ? "codex.closeDiffTab" : "codex.closeFileTab", { path: tab.selection.path })} title={t("common.close")} onClick={() => previews.requestClose(tab)}><X size={13} /></button>
    </div>)}
  </div>;
}

export function SessionPreviewPanels({ previews, workspaceID, workspaceRoot, writable, realtime, notify, onOpenFile, children }: { previews: Previews; workspaceID: string; workspaceRoot: string; writable: boolean; realtime: number; notify: (text: string) => void; onOpenFile: (selection: FilePreviewSelection) => void; children: ReactNode }) {
  return <div className="session-panes">
    <div className="session-conversation" hidden={previews.active !== null}>{children}</div>
    {previews.tabs.map(tab => <div className="session-preview" hidden={previews.active !== tab.key} key={tab.key}>
      {tab.selection.mode === "diff" ? <FileDiffPane workspaceID={workspaceID} selection={tab.selection} realtime={realtime} enabled={previews.active === tab.key} writable={writable} notify={notify} onOpenFile={onOpenFile} onClose={() => previews.remove(tab.key)} /> : <FilePreviewPane workspaceID={workspaceID} workspaceRoot={workspaceRoot} selection={tab.selection} realtime={realtime} enabled={previews.active === tab.key} closeRequest={tab.closeRequest} writable={writable} onOpenFile={onOpenFile} onClose={() => previews.remove(tab.key)} />}
    </div>)}
  </div>;
}
