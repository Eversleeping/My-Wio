import { Archive,ArrowDownToLine,ChevronDown,ChevronRight,Folder,LoaderCircle,MessageSquare,Pin,Plus,Trash2,type LucideIcon } from "lucide-react";
import { useState } from "react";
import { ContextMenu,type ContextMenuAction } from "../ContextMenu";
import { HiddenProjectsButton } from "../components/HiddenProjectsButton";
import { Empty,ErrorState,Status } from "../components/PageUI";
import { useI18n } from "../i18n";
import type { SessionGroup } from "./groups";
import type { Engine,SessionSummary } from "./types";

type Props<T extends SessionSummary> = {
  engine: Engine | "sessions"; icon: LucideIcon; archived: boolean; onArchived: () => void;
  search: string; onSearch: (value: string) => void; revision: unknown;
  refresh: () => void; notify: (text: string) => void;
  groups: SessionGroup<T>[]; selectedID?: string; selectedEngine?: Engine; controls?: React.ReactNode; busy: boolean;
  source: { data?: T[] | null; error: string; loading: boolean; reload: () => void; hasMore: boolean; loadingMore: boolean; loadError: string; loadMore: () => Promise<void> };
  onNew: (workspaceID?: string) => void; onNewInWorkspace?: (workspaceID: string) => void; onSelect: (id: string) => void;
  groupActions?: (group: SessionGroup<T>) => ContextMenuAction[];
  renderRow?: (item: T) => React.ReactNode;
  projectActions: (thread: T) => ContextMenuAction[];
  threadActions: (thread: T) => ContextMenuAction[];
  onDelete: (thread: T) => void; deleteDisabled: (thread: T) => boolean;
};

export function Sidebar<T extends SessionSummary>({ engine, icon: Icon, archived, onArchived, search, onSearch, revision, refresh, notify, groups, selectedID, selectedEngine, controls, busy, source, onNew, onNewInWorkspace, onSelect, projectActions, groupActions, renderRow, threadActions, onDelete, deleteDisabled }: Props<T>) {
  const { t } = useI18n();
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  return <div className={engine === "codex" ? "thread-list" : "thread-list gemini-thread-list"}>
    <div className="panel-heading"><div><Icon size={18} /><h2>{t(archived ? "codex.archivedTasks" : engine === "sessions" ? "nav.sessions" : engine === "codex" ? "codex.sessions" : "gemini.sessions")}</h2></div><div className="row-actions">
      <HiddenProjectsButton realtime={revision} onRestored={refresh} notify={notify} />
      <button className={`icon-button ${archived ? "active" : ""}`} aria-pressed={archived} title={t(engine === "codex" ? archived ? "codex.showActiveTasks" : "codex.showArchivedTasks" : archived ? "gemini.showActive" : "gemini.showArchived")} aria-label={t(engine === "codex" ? archived ? "codex.showActiveTasks" : "codex.showArchivedTasks" : archived ? "gemini.showActive" : "gemini.showArchived")} onClick={onArchived}><Archive size={18} /></button>
      {!archived && <button className="icon-button" title={t(`${engine}.newSession`)} aria-label={t(`${engine}.newSession`)} onClick={() => onNew()}><Plus size={18} /></button>}
    </div></div>
    {engine !== "codex" && <input className="gemini-session-search" type="search" aria-label={t("gemini.searchSessions")} placeholder={t("gemini.searchSessions")} value={search} onChange={event => onSearch(event.target.value)} />}
    {controls}
    <div className="thread-items">{source.error && (engine !== "codex" || !source.data) ? <ErrorState error={source.error} reload={source.reload} /> : source.loading ? <p className="gemini-hint">{t("common.loading")}</p> : <>{groups.length ? groups.map(group => {
      const heading = <button type="button" className="thread-project-toggle" aria-expanded={!collapsed.has(group.id)} title={`${t(collapsed.has(group.id) ? "codex.expandWorkspace" : "codex.collapseWorkspace")} · ${group.serverName} · ${group.path}`} onClick={() => setCollapsed(current => { const next = new Set(current); if (next.has(group.id)) next.delete(group.id); else next.add(group.id); return next; })}>{collapsed.has(group.id) ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><strong><span>{group.name}</span><small>{group.serverName}</small></strong>{group.pinnedAt && <Pin className="pinned-icon" size={12} />}<span>{group.count}</span></button>;
      return <section className="thread-project" key={group.id}>
        {archived && engine !== "codex" && engine !== "sessions" ? <div className="thread-project-heading">{heading}</div> : <ContextMenu className="thread-project-heading" label={t("codex.workspaceMenu", { name: group.name })} actions={groupActions ? groupActions(group) : [{ id: "new-session", label: t("codex.createSessionHere"), icon: Plus, disabled: busy, onSelect: () => (onNewInWorkspace ?? onNew)(group.id) }, ...(group.context ? projectActions(group.context) : [])]}>{heading}</ContextMenu>}
        {!collapsed.has(group.id) && <div className="project-threads">{group.threads.map(item => <ContextMenu key={`${item.engine ?? engine}:${item.id}`} className={`thread ${selectedID === item.id && (!selectedEngine || selectedEngine === item.engine) ? "active" : ""}`} label={t("codex.threadMenu", { name: item.title })} actions={threadActions(item)}>
          {renderRow ? renderRow(item) : <>
          <button className="thread-select" onClick={() => onSelect(item.id)}><span><strong>{item.pinned_at && <Pin size={12} />}{item.title}</strong><small>{item.server_name}</small></span></button>
          <div className="thread-actions"><Status value={item.status} /><button className="icon-button danger thread-delete" aria-label={t(`${engine}.delete`)} disabled={deleteDisabled(item)} onClick={() => onDelete(item)}><Trash2 size={14} /></button></div>
          </>}
        </ContextMenu>)}</div>}
      </section>;
    }) : <Empty icon={<MessageSquare size={24} />} text={t(engine === "sessions" ? search ? "sessions.noMatches" : archived ? "codex.noArchivedTasks" : "sessions.noSessions" : `${engine}.noSessions`)} />}
      {source.loadError && <p role="alert">{source.loadError}</p>}
      {source.hasMore && <div className="history-loader"><button type="button" className="secondary-button small" disabled={source.loadingMore} onClick={() => void source.loadMore()}>{source.loadingMore ? <LoaderCircle className="spin" size={15} /> : <ArrowDownToLine size={15} />}{t(source.loadingMore ? "codex.loadingMoreSessions" : "codex.loadMoreSessions")}</button></div>}
    </>}</div>
  </div>;
}
