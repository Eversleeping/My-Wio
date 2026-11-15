import { Code2,FolderTree,MessageSquare } from "lucide-react";
import { type CSSProperties,type ReactNode,useEffect,useState } from "react";
import type { FilePreviewSelection } from "../codexContent";
import { ErrorState } from "../components/PageUI";
import { SessionCollaboration } from "../components/SessionCollaboration";
import { SessionPreviewPanels,SessionPreviewTabs,useSessionPreviews } from "../components/SessionPreviews";
import { SidebarHeightDivider,readSidebarHeight } from "../components/SidebarHeightDivider";
import { useI18n } from "../i18n";
import { WorkspaceFilesPanel } from "../workspace/WorkspaceFilesPanel";
import type { Engine,SessionSummary } from "./types";

type Props = {
  engine: Engine; thread?: (SessionSummary & { operation_id?: string }) | null;
  selectedID: string; onSelect: (id: string, replace?: boolean) => void;
  revision: number; collaborationRevision: number; notify: (text: string) => void;
  sidebar: (select: (id: string) => void) => ReactNode;
  children: (openFile: (selection: FilePreviewSelection) => void) => ReactNode;
  welcome: ReactNode; dialogs?: ReactNode; error?: string; reload: () => void;
  writable?: boolean;
};

export function readUnifiedSidebarHeight() { return readSidebarHeight("wio_sessions_sidebar_height"); }

export function SessionPage({ engine, thread, selectedID, onSelect, revision, collaborationRevision, notify, sidebar, children, welcome, dialogs, error, reload, writable = true }: Props) {
  const { t } = useI18n();
  const codex = engine === "codex";
  const [mobile, setMobile] = useState(selectedID && codex ? "conversation" : "sessions");
  const key = "wio_sessions_sidebar_height";
  const [height, setHeight] = useState(readUnifiedSidebarHeight);
  useEffect(() => { try { localStorage.setItem(key, String(height)); } catch { /* Resizing remains available without storage. */ } }, [key, height]);
  useEffect(() => { if (selectedID) setMobile("conversation"); }, [selectedID]);
  useEffect(() => { if (codex && thread) setMobile("conversation"); }, [codex, thread?.workspace_id]);
  const previews = useSessionPreviews(`${engine}:${thread?.id ?? ""}:${thread?.workspace_id ?? ""}`, thread?.workspace_id ?? "");
  const openFile = (selection: FilePreviewSelection) => { previews.open(selection); setMobile("conversation"); };
  const select = (id: string) => { onSelect(id); setMobile(id || !codex ? "conversation" : "sessions"); };
  const content = thread || codex ? <>
    <SessionPreviewTabs previews={previews} />
    {thread && <SessionCollaboration key={`collaboration:${engine}:${thread.id}`} engine={engine} id={thread.id} workspaceID={thread.workspace_id} projectID={thread.project_id} revision={collaborationRevision} />}
    <SessionPreviewPanels previews={previews} workspaceID={thread?.workspace_id ?? ""} workspaceRoot={thread?.path ?? ""} writable={writable} realtime={revision} notify={notify} onOpenFile={openFile}>{children(openFile)}</SessionPreviewPanels>
  </> : selectedID && error ? <ErrorState error={error} reload={reload} /> : welcome;
  return <div className={codex ? "codex-layout" : `codex-layout gemini-layout gemini-mobile-${mobile}`}>
    {codex ? <div className="codex-mobile-tabs" role="tablist" aria-label={t("codex.sessionViews")}>{["sessions", "files", "conversation"].map((view, index) => {
      const Icon = [Code2, FolderTree, MessageSquare][index];
      return <button key={view} type="button" role="tab" aria-selected={mobile === view} className={mobile === view ? "active" : ""} onClick={() => setMobile(view)}><Icon size={15} />{t(["codex.sessions", "codex.projectFiles", "codex.conversation"][index])}</button>;
    })}</div> : <div className="gemini-mobile-tabs">{["sessions", "files", "conversation"].map(view => <button key={view} className={mobile === view ? "active" : ""} onClick={() => setMobile(view)}>{t(`gemini.${view}`)}</button>)}</div>}
    <aside className={`codex-sidebar${codex ? ` mobile-${mobile}` : ""}`} style={{ "--thread-list-height": `${height}%` } as CSSProperties}>
      {sidebar(select)}
      {(thread || codex) && <><SidebarHeightDivider value={height} onChange={setHeight} /><WorkspaceFilesPanel workspaceID={thread?.workspace_id ?? null} taskID={codex ? thread?.id ?? "" : thread?.operation_id ?? ""} taskStatus={thread?.status ?? ""} realtime={revision} writable={writable} activePath={previews.preview?.path ?? ""} activeMode={previews.preview?.mode === "diff" ? "diff" : "file"} onOpenFile={openFile} notify={notify} /></>}
    </aside>
    {codex ? <section className={`session-area ${mobile === "conversation" ? "mobile-active" : "mobile-hidden"}`}>{content}</section> : <div className="gemini-main">{content}</div>}
    {dialogs}
  </div>;
}
