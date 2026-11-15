import { type CSSProperties, type ReactNode, useEffect, useState } from "react";
import { SessionCollaboration } from "../components/SessionCollaboration";
import { SessionPreviewPanels, SessionPreviewTabs, useSessionPreviews } from "../components/SessionPreviews";
import { SidebarHeightDivider, readSidebarHeight } from "../components/SidebarHeightDivider";
import { ErrorState } from "../components/PageUI";
import { WorkspaceFilesPanel } from "../workspace/WorkspaceFilesPanel";
import type { FilePreviewSelection } from "../codexContent";
import { useI18n } from "../i18n";
import type { Engine, SessionSummary } from "./types";

type Props = {
  engine: Engine; thread?: (SessionSummary & { operation_id?: string }) | null;
  selectedID: string; onSelect: (id: string, replace?: boolean) => void;
  revision: number; collaborationRevision: number; notify: (text: string) => void;
  sidebar: (select: (id: string) => void) => ReactNode;
  children: (openFile: (selection: FilePreviewSelection) => void) => ReactNode;
  welcome: ReactNode; dialogs?: ReactNode; error?: string; reload: () => void;
};

export function SessionPage({ engine, thread, selectedID, onSelect, revision, collaborationRevision, notify, sidebar, children, welcome, dialogs, error, reload }: Props) {
  const { t } = useI18n();
  const [mobile, setMobile] = useState("sessions");
  const key = `wio_${engine}_sidebar_height`;
  const [height, setHeight] = useState(() => readSidebarHeight(key));
  useEffect(() => { try { localStorage.setItem(key, String(height)); } catch { /* Resizing remains available without storage. */ } }, [key, height]);
  useEffect(() => { if (selectedID) setMobile("conversation"); }, [selectedID]);
  const previews = useSessionPreviews(`${engine}:${thread?.id ?? ""}:${thread?.workspace_id ?? ""}`, thread?.workspace_id ?? "");
  const openFile = (selection: FilePreviewSelection) => { previews.open(selection); setMobile("conversation"); };
  const select = (id: string) => { onSelect(id); setMobile("conversation"); };
  return <div className={`codex-layout gemini-layout gemini-mobile-${mobile}`}>
    <div className="gemini-mobile-tabs">{["sessions", "files", "conversation"].map(view => <button key={view} className={mobile === view ? "active" : ""} onClick={() => setMobile(view)}>{t(`gemini.${view}`)}</button>)}</div>
    <aside className="codex-sidebar" style={{ "--thread-list-height": `${height}%` } as CSSProperties}>
      {sidebar(select)}
      {thread && <SidebarHeightDivider value={height} onChange={setHeight} />}
      {thread && <WorkspaceFilesPanel workspaceID={thread.workspace_id} taskID={thread.operation_id ?? ""} taskStatus={thread.status} realtime={revision} activePath={previews.preview?.path ?? ""} activeMode={previews.preview?.mode === "diff" ? "diff" : "file"} onOpenFile={openFile} notify={notify} />}
    </aside>
    <div className="gemini-main">{thread ? <>
      <SessionPreviewTabs previews={previews} />
      <SessionCollaboration key={`collaboration:${engine}:${thread.id}`} engine={engine} id={thread.id} workspaceID={thread.workspace_id} projectID={thread.project_id} revision={collaborationRevision} />
      <SessionPreviewPanels previews={previews} workspaceID={thread.workspace_id} workspaceRoot={thread.path} writable realtime={revision} notify={notify} onOpenFile={openFile}>{children(openFile)}</SessionPreviewPanels>
    </> : selectedID && error ? <ErrorState error={error} reload={reload} /> : welcome}</div>
    {dialogs}
  </div>;
}
