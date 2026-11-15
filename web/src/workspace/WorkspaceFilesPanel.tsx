import { useVisibleInterval } from "../hooks/useVisibleInterval";
import { type FilePreviewSelection } from "../codexContent";
import { useI18n } from "../i18n";
import { useState, useRef, useEffect, useCallback, useMemo } from "react";
import { useData } from "../useData";
import { type WorkspaceFilesSnapshot, type WorkspaceChangesSnapshot, type WorkspaceGitSnapshot } from "../types";
import { post } from "../api";
import { message } from "../text";
import { buildFileTree, type FileTreeNode } from "./fileTree";
import { AddWorkspaceFileDialog } from "../workspaceFileEditing";
import {
  FileDiff,
  FolderTree,
  FilePlus2,
  LoaderCircle,
  RefreshCw,
  AlertTriangle,
  Folder,
  GitCommit,
  ArrowRightLeft,
  ArrowUpFromLine,
  ChevronDown,
  ChevronRight,
  FolderOpen,
  File as FileIcon
} from "lucide-react";
import { Empty } from "../components/PageUI";
import { VirtualizedList } from "../components/VirtualizedList";

export function WorkspaceFilesPanel({ workspaceID, taskID, taskStatus, realtime, notify, writable = true, activePath, activeMode, onOpenFile }: { workspaceID: string | null; taskID: string; taskStatus: string; realtime: number; notify: (text: string) => void; writable?: boolean; activePath: string; activeMode: "file" | "diff"; onOpenFile: (selection: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  const [mode, setMode] = useState<"files" | "changes">("files");
  const snapshot = useData<WorkspaceFilesSnapshot>(workspaceID ? `/workspaces/${workspaceID}/files` : null, `${realtime}:${workspaceID}`);
  const changes = useData<WorkspaceChangesSnapshot>(workspaceID && mode === "changes" ? `/workspaces/${workspaceID}/changes` : null, `${realtime}:${workspaceID}:${mode}`);
  const git = useData<WorkspaceGitSnapshot>(workspaceID && mode === "changes" ? `/workspaces/${workspaceID}/git` : null, `${realtime}:${workspaceID}:${mode}`);
  const [requestedWorkspace, setRequestedWorkspace] = useState("");
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [commitMessage, setCommitMessage] = useState("");
  const [gitAction, setGitAction] = useState<"" | "commit" | "pull" | "push">("");
  const [addingFile, setAddingFile] = useState(false);
  const previousTask = useRef({ id: taskID, status: taskStatus });
  const syncedGitUpdate = useRef("");
  const currentSnapshot = snapshot.data?.workspace_id === workspaceID ? snapshot.data : null;
  const currentChanges = changes.data?.workspace_id === workspaceID ? changes.data : null;
  const currentGit = git.data?.workspace_id === workspaceID ? git.data : null;
  const gitData = currentGit?.data;
  const remote = gitData?.remotes?.[0]?.name ?? "";
  const branch = gitData?.status.branch ?? "";
  const fileScanning = currentSnapshot?.status === "scanning";
  const changeScanning = currentChanges?.status === "scanning";
  // File-scan expiry is persisted by maintenance without a realtime event.
  // Read only outstanding scans so connected clients also observe the timeout.
  useVisibleInterval(snapshot.reload, workspaceID && fileScanning ? 5000 : null);
  const refreshFiles = useCallback(async (silent = false) => {
    if (!workspaceID) return;
    try {
      await post(`/workspaces/${workspaceID}/files/refresh`, {});
      snapshot.reload();
      if (!silent) notify(t("codex.fileScanQueued"));
    } catch (error) {
      notify(message(error));
    }
  }, [notify, snapshot.reload, t, workspaceID]);
  const refreshChanges = useCallback(async (silent = false) => {
    if (!workspaceID) return;
    try {
      await post(`/workspaces/${workspaceID}/changes/refresh`, {});
      changes.reload();
      if (!silent) notify(t("codex.changeScanQueued"));
    } catch (error) {
      notify(message(error));
    }
  }, [changes.reload, notify, t, workspaceID]);
  const refreshGit = useCallback(async (silent = false) => {
    if (!workspaceID) return;
    try {
      await post(`/workspaces/${workspaceID}/git/refresh`, {});
      git.reload();
      if (!silent) notify(t("project.gitRefreshQueued"));
    } catch (error) {
      notify(message(error));
    }
  }, [git.reload, notify, t, workspaceID]);
  useEffect(() => {
    setMode("files");
    setExpanded(new Set());
    setRequestedWorkspace("");
    setCommitMessage("");
    setGitAction("");
    setAddingFile(false);
    syncedGitUpdate.current = "";
  }, [workspaceID]);
  useEffect(() => {
    if (workspaceID && currentSnapshot?.status === "idle" && requestedWorkspace !== workspaceID) {
      setRequestedWorkspace(workspaceID);
      void refreshFiles(true);
    }
  }, [currentSnapshot?.status, refreshFiles, requestedWorkspace, workspaceID]);
  useEffect(() => {
    const previous = previousTask.current;
    previousTask.current = { id: taskID, status: taskStatus };
    const wasActive = previous.status === "queued" || previous.status === "running";
    const isActive = taskStatus === "queued" || taskStatus === "running";
    if (mode === "changes" && workspaceID && previous.id === taskID && wasActive && !isActive) void refreshChanges(true);
  }, [mode, refreshChanges, taskID, taskStatus, workspaceID]);
  useEffect(() => {
    const gitUpdated = currentGit?.updated_at ?? "";
    const changesUpdated = currentChanges?.updated_at ?? "";
    if (mode !== "changes" || currentGit?.status !== "succeeded" || !gitUpdated || gitUpdated <= changesUpdated || syncedGitUpdate.current === gitUpdated) return;
    syncedGitUpdate.current = gitUpdated;
    void refreshChanges(true);
  }, [currentChanges?.updated_at, currentGit?.status, currentGit?.updated_at, mode, refreshChanges]);
  const tree = useMemo(() => buildFileTree(currentSnapshot?.files ?? []), [currentSnapshot?.files]);
  const toggle = (path: string) => setExpanded(current => { const next = new Set(current); if (next.has(path)) next.delete(path); else next.add(path); return next; });
  const toggleMode = () => {
    if (mode === "changes") {
      setMode("files");
      return;
    }
    setMode("changes");
    void refreshChanges(true);
    void refreshGit(true);
  };
  const runGitAction = async (action: "commit" | "pull" | "push") => {
    if (!workspaceID || gitAction) return;
    const messageText = commitMessage.trim();
    if (action === "commit" && !messageText) return;
    setGitAction(action);
    try {
      if (action === "commit") {
        await post(`/workspaces/${workspaceID}/git/commit`, { message: messageText, all: true });
        setCommitMessage("");
      } else if (action === "pull") {
        await post(`/workspaces/${workspaceID}/git/pull`, { remote, branch });
      } else {
        await post(`/workspaces/${workspaceID}/git/push`, { remote, ref: branch, set_upstream: !gitData?.status.upstream });
      }
      notify(t("project.gitActionQueued"));
      git.reload();
      changes.reload();
    } catch (error) {
      notify(message(error));
    } finally {
      setGitAction("");
    }
  };
  const busy = mode === "changes" ? changeScanning || currentGit?.status === "refreshing" : fileScanning;
  const refresh = () => {
    if (mode === "changes") {
      void refreshChanges();
      void refreshGit(true);
    } else {
      void refreshFiles();
    }
  };
  const changeCount = currentChanges?.changes.length ?? 0;
  const writeBusy = Boolean(gitAction) || currentGit?.status === "refreshing";
  const writeDisabled = !workspaceID || !writable || writeBusy;
  return <section className={`workspace-files ${mode === "changes" ? "changes-mode" : ""}`}>
    {addingFile && workspaceID && <AddWorkspaceFileDialog key={workspaceID} workspaceID={workspaceID} onClose={() => setAddingFile(false)} onCreated={path => { snapshot.reload(); changes.reload(); onOpenFile({ path, mode: "file" }); }} />}
    <div className="panel-heading"><div>{mode === "changes" ? <FileDiff size={17} /> : <FolderTree size={17} />}<h2>{t(mode === "changes" ? "codex.changedFiles" : "codex.projectFiles")}</h2></div><div className="row-actions"><button type="button" className="icon-button" disabled={!workspaceID || !writable} title={t("codex.addFile")} aria-label={t("codex.addFile")} onClick={() => setAddingFile(true)}><FilePlus2 size={16} /></button><button className={`icon-button ${mode === "changes" ? "active" : ""}`} type="button" aria-pressed={mode === "changes"} disabled={!workspaceID} title={t(mode === "changes" ? "codex.showProjectFiles" : "codex.showChangedFiles")} onClick={toggleMode}><FileDiff size={16} /></button><button className="icon-button" type="button" disabled={!workspaceID || busy} title={t(mode === "changes" ? "codex.refreshChanges" : "codex.refreshFiles")} onClick={refresh}>{busy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div></div>
    <div className="workspace-file-body">{mode === "changes" ? <ChangedFilesView workspaceID={workspaceID} snapshot={currentChanges} loading={changes.loading} activePath={activeMode === "diff" ? activePath : ""} onOpenFile={path => onOpenFile({ path, mode: "diff" })} /> : !workspaceID ? <Empty icon={<FolderTree size={22} />} text={t("codex.selectWorkspace")} /> : !currentSnapshot ? <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningFiles")}</div> : currentSnapshot.status === "failed" ? <div className="file-tree-error"><AlertTriangle size={16} /><span>{currentSnapshot.error || t("codex.fileScanFailed")}</span></div> : fileScanning && tree.length === 0 ? <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningFiles")}</div> : tree.length === 0 ? <Empty icon={<Folder size={22} />} text={t("codex.noProjectFiles")} /> : <div className="file-tree">{tree.map(node => <FileTreeItem key={node.path} node={node} depth={0} expanded={expanded} onToggle={toggle} activePath={activeMode === "file" ? activePath : ""} onOpenFile={path => onOpenFile({ path, mode: "file" })} />)}{currentSnapshot.truncated && <div className="file-tree-note">{t("codex.fileListTruncated")}</div>}</div>}</div>
    {mode === "changes" && <div className="workspace-change-actions">
      <form onSubmit={event => { event.preventDefault(); void runGitAction("commit"); }}><input aria-label={t("project.gitCommitMessage")} value={commitMessage} maxLength={20 * 1024} disabled={writeDisabled} placeholder={t("project.gitCommitPlaceholder")} onChange={event => setCommitMessage(event.target.value)} /><button className="primary-button small" disabled={writeDisabled || changeCount === 0 || !commitMessage.trim()}>{gitAction === "commit" ? <LoaderCircle className="spin" size={14} /> : <GitCommit size={14} />}{t("project.gitCommitAction")}</button></form>
      <div><button type="button" className="secondary-button small" disabled={writeDisabled || changeCount > 0 || !remote || !branch} title={changeCount > 0 ? t("codex.pullBlockedByChanges") : t("project.gitPull")} onClick={() => void runGitAction("pull")}>{gitAction === "pull" ? <LoaderCircle className="spin" size={14} /> : <ArrowRightLeft size={14} />}{t("project.gitPull")}</button><button type="button" className="secondary-button small" disabled={writeDisabled || !remote || !branch} title={t("project.gitPush")} onClick={() => void runGitAction("push")}>{gitAction === "push" ? <LoaderCircle className="spin" size={14} /> : <ArrowUpFromLine size={14} />}{t("project.gitPush")}</button></div>
    </div>}
  </section>;
}

const changeStatusKeys: Record<string, string> = { modified: "codex.changeModified", added: "codex.changeAdded", deleted: "codex.changeDeleted", renamed: "codex.changeRenamed", copied: "codex.changeCopied", untracked: "codex.changeAdded", conflicted: "codex.changeConflicted" };

const changeStatusCodes: Record<string, string> = { modified: "M", added: "A", deleted: "D", renamed: "R", copied: "C", untracked: "?", conflicted: "!" };

export function ChangedFilesView({ workspaceID, snapshot, loading, activePath, onOpenFile }: { workspaceID: string | null; snapshot: WorkspaceChangesSnapshot | null; loading: boolean; activePath: string; onOpenFile: (path: string) => void }) {
  const { t } = useI18n();
  if (!workspaceID) return <Empty icon={<FileDiff size={22} />} text={t("codex.selectWorkspace")} />;
  if (loading || !snapshot) return <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningChanges")}</div>;
  if (snapshot.status === "failed") return <div className="file-tree-error"><AlertTriangle size={16} /><span>{snapshot.error || t("codex.changeScanFailed")}</span></div>;
  if (snapshot.status === "scanning" && snapshot.changes.length === 0) return <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningChanges")}</div>;
  if (snapshot.changes.length === 0) return <Empty icon={<FileDiff size={22} />} text={t("codex.noChanges")} />;
  return <VirtualizedList
    className="change-file-list"
    items={snapshot.changes}
    getKey={change => change.path}
    estimateSize={42}
    renderItem={change => {
      const name = change.path.split("/").pop() || change.path;
      return <button type="button" className={`change-file-row ${activePath === change.path ? "active" : ""}`} title={change.path} onClick={() => onOpenFile(change.path)}><span className={`change-file-status ${change.status}`}>{changeStatusCodes[change.status] ?? "M"}</span><span className="change-file-name"><strong>{name}</strong><small>{change.path}</small></span><span className="change-file-label">{t(changeStatusKeys[change.status] ?? "codex.changeModified")}</span></button>;
    }}
  />;
}

function FileTreeItem({ node, depth, expanded, onToggle, activePath, onOpenFile }: { node: FileTreeNode; depth: number; expanded: Set<string>; onToggle: (path: string) => void; activePath: string; onOpenFile: (path: string) => void }) {
  const directory = node.kind === "directory";
  const open = directory && expanded.has(node.path);
  const content = <><span className="file-tree-chevron">{directory ? open ? <ChevronDown size={13} /> : <ChevronRight size={13} /> : null}</span>{directory ? open ? <FolderOpen size={15} /> : <Folder size={15} /> : <FileIcon size={14} />}<span title={node.path}>{node.name}</span></>;
  return <>{<button type="button" className={`file-tree-row ${!directory && activePath === node.path ? "active" : ""}`} style={{ paddingLeft: 8 + depth * 14 }} onClick={() => directory ? onToggle(node.path) : onOpenFile(node.path)}>{content}</button>}{open && node.children.map(child => <FileTreeItem key={child.path} node={child} depth={depth + 1} expanded={expanded} onToggle={onToggle} activePath={activePath} onOpenFile={onOpenFile} />)}</>;
}
