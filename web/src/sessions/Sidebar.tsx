import { Archive, ArrowDownToLine, ChevronDown, ChevronRight, Folder, LoaderCircle, MessageSquare, Pin, Plus, type LucideIcon } from "lucide-react";
import { useState } from "react";
import { ContextMenu, type ContextMenuAction } from "../ContextMenu";
import { HiddenProjectsButton } from "../components/HiddenProjectsButton";
import { Empty, ErrorState } from "../components/PageUI";
import { useI18n } from "../i18n";
import type { SessionGroup } from "./groups";
import type { Engine,SessionSummary } from "./types";

type Props<T extends SessionSummary> = {
  icon: LucideIcon; archived: boolean; onArchived: () => void;
  search: string; onSearch: (value: string) => void; revision: unknown;
  refresh: () => void; notify: (text: string) => void;
  focusWorkspaceID?: string; groups: SessionGroup<T>[]; selectedID?: string; selectedEngine?: Engine; controls?: React.ReactNode;
  source: { data?: T[] | null; error: string; loading: boolean; reload: () => void; hasMore: boolean; loadingMore: boolean; loadError: string; loadMore: () => Promise<void> };
  onNew: (workspaceID?: string) => void;
  groupActions: (group: SessionGroup<T>) => ContextMenuAction[];
  renderRow: (item: T) => React.ReactNode;
  threadActions: (thread: T) => ContextMenuAction[];
};

export function Sidebar<T extends SessionSummary>({ icon: Icon, archived, onArchived, search, onSearch, revision, refresh, notify, focusWorkspaceID, groups, selectedID, selectedEngine, controls, source, onNew, groupActions, renderRow, threadActions }: Props<T>) {
  const { t } = useI18n();
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  return <div className="thread-list gemini-thread-list">
    <div className="panel-heading"><div><Icon size={18} /><h2>{t(archived ? "codex.archivedTasks" : "nav.sessions")}</h2></div><div className="row-actions">
      <HiddenProjectsButton realtime={revision} onRestored={refresh} notify={notify} />
      <button className={`icon-button ${archived ? "active" : ""}`} aria-pressed={archived} title={t(archived ? "gemini.showActive" : "gemini.showArchived")} aria-label={t(archived ? "gemini.showActive" : "gemini.showArchived")} onClick={onArchived}><Archive size={18} /></button>
      {!archived && <button className="icon-button" title={t("sessions.newSession")} aria-label={t("sessions.newSession")} onClick={() => onNew()}><Plus size={18} /></button>}
    </div></div>
    <input className="gemini-session-search" type="search" aria-label={t("gemini.searchSessions")} placeholder={t("gemini.searchSessions")} value={search} onChange={event => onSearch(event.target.value)} />
    {controls}
    <div className="thread-items">{source.error ? <ErrorState error={source.error} reload={source.reload} /> : source.loading ? <p className="gemini-hint">{t("common.loading")}</p> : <>{groups.length ? groups.map(group => {
      const heading = <button id={`session-workspace-${group.id}`} type="button" className={`thread-project-toggle${focusWorkspaceID === group.id ? " workspace-focused" : ""}`} aria-expanded={!collapsed.has(group.id)} title={`${t(collapsed.has(group.id) ? "codex.expandWorkspace" : "codex.collapseWorkspace")} · ${group.serverName} · ${group.path}`} onClick={() => setCollapsed(current => { const next = new Set(current); if (next.has(group.id)) next.delete(group.id); else next.add(group.id); return next; })}>{collapsed.has(group.id) ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><strong><span>{group.name}</span><small>{group.serverName}</small></strong>{group.pinnedAt && <Pin className="pinned-icon" size={12} />}<span>{group.count}</span></button>;
      return <section className="thread-project" key={group.id}>
        <ContextMenu className="thread-project-heading" label={t("codex.workspaceMenu", { name: group.name })} actions={groupActions(group)}>{heading}</ContextMenu>
        {!collapsed.has(group.id) && <div className="project-threads">{group.threads.map(item => <ContextMenu key={`${item.engine}:${item.id}`} className={`thread ${selectedID === item.id && (!selectedEngine || selectedEngine === item.engine) ? "active" : ""}`} label={t("codex.threadMenu", { name: item.title })} actions={threadActions(item)}>
          {renderRow(item)}
        </ContextMenu>)}</div>}
      </section>;
    }) : <Empty icon={<MessageSquare size={24} />} text={t(search ? "sessions.noMatches" : archived ? "codex.noArchivedTasks" : "sessions.noSessions")} />}
      {source.loadError && <p role="alert">{source.loadError}</p>}
      {source.hasMore && <div className="history-loader"><button type="button" className="secondary-button small" disabled={source.loadingMore} onClick={() => void source.loadMore()}>{source.loadingMore ? <LoaderCircle className="spin" size={15} /> : <ArrowDownToLine size={15} />}{t(source.loadingMore ? "codex.loadingMoreSessions" : "codex.loadMoreSessions")}</button></div>}
    </>}</div>
  </div>;
}
