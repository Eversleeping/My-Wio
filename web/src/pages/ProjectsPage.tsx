import { type ReactNode, useEffect, useState } from "react";
import { Boxes, GitBranch, LoaderCircle, Pencil, Plus, RefreshCw, RotateCcw, Settings, Trash2 } from "lucide-react";
import { api, patch, post, remove } from "../api";
import { Dialog as AccessibleDialog, DialogActions, type DialogProps } from "../components/Dialog";
import { DataTable, Section, Status } from "../components/PageUI";
import { relative, shortSHA } from "../format";
import { useI18n } from "../i18n";
import {
  CreateProjectDialog,
  ProjectDeletionDialog,
  ProjectDetailsDialog,
  ProjectTable,
  WorkspaceGitDialog,
  WorkspaceManagerDialog,
  WorkspaceTable,
  newCreateProjectFormValue,
  type CreateProjectDialogLabels,
  type CreateProjectRequest,
  type ProjectEditValue,
  type ProjectLifecycleState,
  type ProjectListRecord,
  type ProjectDeletionMode,
  type WorkspaceDeletionMode,
  type WorkspaceGitAction
} from "./projects";
import type { Project, ProjectDeletionPlan, ProjectDetail, Server, Workspace, WorkspaceDeletionPlan, WorkspaceGitSnapshot } from "../types";
import { useData } from "../useData";

export interface ProjectsPageProps {
  realtime: number;
  notify: (text: string) => void;
}

function readResourceFilters() {
  const params = new URLSearchParams(window.location.search);
  return { projectName: params.get("project_name") ?? "", projectServerID: params.get("project_server_id") ?? "", projectStatus: params.get("project_status") ?? "", workspacePath: params.get("workspace_path") ?? "", workspaceServerID: params.get("workspace_server_id") ?? "", workspaceGitStatus: params.get("workspace_git_status") ?? "" };
}

function resourcePath(path: string, values: Record<string, string>) {
  const params = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => { if (value) params.set(key, value); });
  const query = params.toString();
  return query ? `${path}?${query}` : path;
}

export function ProjectsPage({ realtime, notify }: ProjectsPageProps) {
  const { t } = useI18n();
  const initialFilters = readResourceFilters();
  const [projectName, setProjectName] = useState(initialFilters.projectName);
  const [projectServerID, setProjectServerID] = useState(initialFilters.projectServerID);
  const [projectStatus, setProjectStatus] = useState(initialFilters.projectStatus);
  const [workspacePath, setWorkspacePath] = useState(initialFilters.workspacePath);
  const [workspaceServerID, setWorkspaceServerID] = useState(initialFilters.workspaceServerID);
  const [workspaceGitStatus, setWorkspaceGitStatus] = useState(initialFilters.workspaceGitStatus);
  const projects = useData<Project[]>(resourcePath("/projects", { name: projectName, server_id: projectServerID, status: projectStatus }), realtime);
  const workspaces = useData<Workspace[]>(resourcePath("/workspaces", { path: workspacePath, server_id: workspaceServerID, git_status: workspaceGitStatus }), realtime);
  const servers = useData<Server[]>("/servers", realtime);
  const [dialog, setDialog] = useState(false);
  const [busy, setBusy] = useState(false);
  const [createError, setCreateError] = useState("");
  const [form, setForm] = useState(newCreateProjectFormValue());
  const [projectAction, setProjectAction] = useState<{ id: string; kind: "retry" | "restore" } | null>(null);
  const [detailProjectID, setDetailProjectID] = useState<string | null>(null);
  const [detailBusy, setDetailBusy] = useState(false);
  const [detailError, setDetailError] = useState("");
  const detail = useData<ProjectDetail>(detailProjectID ? `/projects/${detailProjectID}` : null, realtime);
  const [deleteProjectID, setDeleteProjectID] = useState<string | null>(null);
  const [projectDeletionPlan, setProjectDeletionPlan] = useState<ProjectDeletionPlan | null>(null);
  const [projectDeletionBusy, setProjectDeletionBusy] = useState(false);
  const [projectDeletionError, setProjectDeletionError] = useState("");
  const [manageWorkspaceID, setManageWorkspaceID] = useState<string | null>(null);
  const [workspaceDeletionPlan, setWorkspaceDeletionPlan] = useState<WorkspaceDeletionPlan | null>(null);
  const [workspaceManagerBusy, setWorkspaceManagerBusy] = useState(false);
  const [workspacePlanLoading, setWorkspacePlanLoading] = useState(false);
  const [workspaceManagerError, setWorkspaceManagerError] = useState("");

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const values: Record<string, string> = { project_name: projectName, project_server_id: projectServerID, project_status: projectStatus, workspace_path: workspacePath, workspace_server_id: workspaceServerID, workspace_git_status: workspaceGitStatus };
    Object.entries(values).forEach(([key, value]) => { if (value) params.set(key, value); else params.delete(key); });
    const query = params.toString();
    const next = `${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`;
    const current = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    if (next !== current) window.history.replaceState(null, "", next);
  }, [projectName, projectServerID, projectStatus, workspacePath, workspaceServerID, workspaceGitStatus]);

  const clearFilters = () => { setProjectName(""); setProjectServerID(""); setProjectStatus(""); setWorkspacePath(""); setWorkspaceServerID(""); setWorkspaceGitStatus(""); };
  const filtered = Boolean(projectName || projectServerID || projectStatus || workspacePath || workspaceServerID || workspaceGitStatus);

  const openDialog = () => {
    setForm(newCreateProjectFormValue());
    setCreateError("");
    setDialog(true);
  };
  const close = () => { if (!busy) setDialog(false); };
  const submit = async (request: CreateProjectRequest) => {
    setBusy(true);
    setCreateError("");
    try {
      if (request.mode === "blank") {
        await post("/projects", request);
        notify(t("project.createQueued"));
      } else if (request.mode === "clone") {
        await post("/projects/import", {
          remote_url: request.remote_url,
          name: request.name ?? "",
          server_id: request.server_id,
          destination: request.destination ?? ""
        });
        notify(t("project.queued"));
      } else {
        await post("/projects/discover", { server_id: request.server_id });
        notify(t("project.scanQueued"));
      }
      projects.reload();
      workspaces.reload();
      setDialog(false);
    } catch (err) {
      const error = message(err);
      setCreateError(error);
      notify(error);
    } finally { setBusy(false); }
  };
  const retryProject = async (project: Project) => {
    setProjectAction({ id: project.id, kind: "retry" });
    try {
      const blank = project.status === "failed" || project.status === "partial";
      await post(`/projects/${project.id}/${blank ? "retry-create" : "retry-import"}`, {});
      projects.reload();
      notify(t(blank ? "project.createRetryQueued" : "project.retryQueued"));
    } catch (err) { notify(message(err)); } finally { setProjectAction(null); }
  };
  const openProjectDeletion = async (project: Project) => {
    setDeleteProjectID(project.id);
    setProjectDeletionPlan(null);
    setProjectDeletionError("");
    setProjectDeletionBusy(true);
    try { setProjectDeletionPlan(await post<ProjectDeletionPlan>(`/projects/${project.id}/deletion-plan`, {})); }
    catch (err) { setProjectDeletionError(message(err)); }
    finally { setProjectDeletionBusy(false); }
  };
  const deleteProject = async (mode: ProjectDeletionMode) => {
    if (!deleteProjectID) return;
    setProjectDeletionBusy(true);
    setProjectDeletionError("");
    try {
      await api(`/projects/${deleteProjectID}`, { method: "DELETE", body: JSON.stringify({ mode }) });
      setDeleteProjectID(null);
      setProjectDeletionPlan(null);
      projects.reload();
      workspaces.reload();
      notify(t(mode === "managed-files" ? "project.deleteFilesQueued" : "project.deleted"));
    } catch (err) { setProjectDeletionError(message(err)); }
    finally { setProjectDeletionBusy(false); }
  };
  const restoreProject = async (project: Project) => {
    setProjectAction({ id: project.id, kind: "restore" });
    try {
      await patch<Project>(`/projects/${project.id}`, { hidden: false });
      projects.reload();
      notify(t("project.restored"));
    } catch (err) { notify(message(err)); } finally { setProjectAction(null); }
  };
  const saveProjectDetails = async (value: ProjectEditValue) => {
    if (!detailProjectID) return;
    setDetailBusy(true);
    setDetailError("");
    try {
      await patch(`/projects/${detailProjectID}`, { name: value.name.trim(), description: value.description.trim(), default_branch: value.defaultBranch.trim(), pinned: value.pinned, hidden: value.hidden, archived: value.archived });
      projects.reload();
      detail.reload();
      setDetailProjectID(null);
      notify(t("project.saved"));
    } catch (err) { setDetailError(message(err)); } finally { setDetailBusy(false); }
  };
  const importMessage = (project: ProjectListRecord) => {
    const raw = project.provision_error || project.import_message;
    return /http2 framing|expected flush|timed? out|timeout|could not resolve|temporary failure in name resolution|connection (?:refused|reset)|network is unreachable|dial tcp/i.test(raw) ? t("project.networkFailure") : raw;
  };
  const serverOptions = (servers.data ?? []).map(server => ({ id: server.id, name: server.name, status: server.status }));
  const labels: CreateProjectDialogLabels = {
    title: t("project.createTitle"), modeLabel: t("project.createMode"), blankMode: t("project.blankMode"), cloneMode: t("project.cloneMode"), discoverMode: t("project.discoverMode"),
    projectName: t("project.name"), targetServer: t("project.targetServer"), selectServer: t("project.selectServer"), offline: t("status.offline"), destination: t("project.destination"), optional: t("common.optional"), initialBranch: t("project.initialBranch"),
    remoteSetup: t("project.remoteSetup"), remoteNone: t("project.remoteNone"), remoteExisting: t("project.remoteExisting"), remoteCreate: t("project.remoteCreate"), remoteURL: t("project.remoteURL"), remoteProvider: t("project.remoteProvider"), remoteNamespace: t("project.remoteNamespace"), remoteRepository: t("project.remoteRepository"), remoteVisibility: t("project.remoteVisibility"), visibilityPrivate: t("project.visibilityPrivate"), visibilityInternal: t("project.visibilityInternal"), visibilityPublic: t("project.visibilityPublic"), initializeReadme: t("project.initializeReadme"), existingServer: t("project.existingServer"), comingSoon: t("project.comingSoon"), cancel: t("common.cancel"), working: t("project.working"), create: t("project.create"), clone: t("project.queue"), discover: t("project.scan"),
    nameRequired: t("project.nameRequired"), serverRequired: t("project.serverRequired"), remoteURLRequired: t("project.remoteURLRequired"), initialBranchRequired: t("project.initialBranchRequired"), remoteProviderRequired: t("project.remoteProviderRequired"), remoteRepositoryRequired: t("project.remoteRepositoryRequired"), remoteUnavailable: t("project.remoteUnavailable")
  };
  const detailLabels = {
    title: t("project.detailTitle"), overview: t("project.detailOverview"), history: t("project.detailHistory"), name: t("project.name"), description: t("project.description"), defaultBranch: t("project.defaultBranch"), pinned: t("project.pin"), hidden: t("project.hide"), archived: t("project.archive"), remote: t("column.remote"), noRemote: t("project.noRemote"), operation: t("project.operation"), state: t("project.status"), time: t("column.updated"), result: t("project.result"), noOperations: t("project.noOperations"), cancel: t("common.cancel"), save: t("common.save"), saving: t("common.saving"), loading: t("common.loading")
  };
  const projectLabels = { project: t("column.project"), remote: t("column.remote"), workspaces: t("column.workspaces"), status: t("project.status"), updated: t("column.updated"), actions: t("common.actions"), empty: t("project.none"), local: t("project.local"), hidden: t("project.hidden"), targetServer: (server: string) => t("project.targetSummary", { server }), awaitingWorkspace: t("project.awaitingWorkspace") };
  const workspaceLabels = { project: t("column.project"), server: t("column.server"), path: t("column.path"), branch: t("column.branch"), commit: t("column.commit"), state: t("column.state"), actions: t("common.actions"), empty: t("project.noWorkspaces"), detached: t("project.detached") };
  const [gitWorkspaceID, setGitWorkspaceID] = useState<string | null>(null);
  const [gitBusy, setGitBusy] = useState(false);
  const [gitError, setGitError] = useState("");
  const gitSnapshot = useData<WorkspaceGitSnapshot>(gitWorkspaceID ? `/workspaces/${gitWorkspaceID}/git` : null, realtime);
  const refreshWorkspaceGit = async (workspaceID = gitWorkspaceID, announce = true) => {
    if (!workspaceID) return;
    setGitBusy(true);
    setGitError("");
    try { await post(`/workspaces/${workspaceID}/git/refresh`, {}); gitSnapshot.reload(); if (announce) notify(t("project.gitRefreshQueued")); } catch (err) { setGitError(message(err)); } finally { setGitBusy(false); }
  };
  const openWorkspaceGit = (workspaceID: string) => {
    setGitError("");
    setGitWorkspaceID(workspaceID);
    void refreshWorkspaceGit(workspaceID, false);
  };
  const runGitAction = async (action: WorkspaceGitAction) => {
    if (!gitWorkspaceID) return;
    const base = `/workspaces/${gitWorkspaceID}/git`;
    const segment = (value: string) => encodeURIComponent(value);
    setGitBusy(true);
    setGitError("");
    try {
      switch (action.type) {
        case "branch.create": await post(`${base}/branches`, { name: action.name, start_point: action.startPoint }); break;
        case "branch.rename": await patch(`${base}/branches/${segment(action.branch)}`, { name: action.name }); break;
        case "branch.delete": await remove(`${base}/branches/${segment(action.branch)}?force=${action.force}`); break;
        case "checkout": await post(`${base}/checkout`, { ref: action.ref, detach: action.detach }); break;
        case "remote.add": await post(`${base}/remotes`, { name: action.name, url: action.url }); break;
        case "remote.update": await patch(`${base}/remotes/${segment(action.remote)}`, { url: action.url }); break;
        case "remote.delete": await remove(`${base}/remotes/${segment(action.remote)}`); break;
        case "fetch": await post(`${base}/fetch`, { remote: action.remote }); break;
        case "pull": await post(`${base}/pull`, { remote: action.remote, branch: action.branch }); break;
        case "push": await post(`${base}/push`, { remote: action.remote, ref: action.ref, set_upstream: action.setUpstream }); break;
        case "stage": await post(`${base}/stage`, { paths: action.paths, all: action.all }); break;
        case "unstage": await post(`${base}/unstage`, { paths: action.paths, all: action.all }); break;
        case "discard": await post(`${base}/discard`, { paths: action.paths, all: action.all }); break;
        case "commit": await post(`${base}/commit`, { message: action.message }); break;
      }
      notify(t("project.gitActionQueued"));
      gitSnapshot.reload();
    } catch (err) {
      const detail = message(err);
      setGitError(detail);
      throw err;
    } finally { setGitBusy(false); }
  };
  const openWorkspaceManager = (workspace: Workspace) => {
    setManageWorkspaceID(workspace.id);
    setWorkspaceDeletionPlan(null);
    setWorkspaceManagerError("");
  };
  const renameWorkspace = async (name: string) => {
    if (!manageWorkspaceID) return;
    setWorkspaceManagerBusy(true); setWorkspaceManagerError("");
    try { await patch(`/workspaces/${manageWorkspaceID}`, { display_name: name }); workspaces.reload(); setManageWorkspaceID(null); notify(t("project.workspaceSaved")); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspaceManagerBusy(false); }
  };
  const moveWorkspace = async (path: string) => {
    if (!manageWorkspaceID) return;
    setWorkspaceManagerBusy(true); setWorkspaceManagerError("");
    try { await post(`/workspaces/${manageWorkspaceID}/move`, { path }); workspaces.reload(); setManageWorkspaceID(null); notify(t("project.workspaceMoveQueued")); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspaceManagerBusy(false); }
  };
  const copyWorkspace = async (serverID: string, path: string) => {
    if (!manageWorkspaceID) return;
    setWorkspaceManagerBusy(true); setWorkspaceManagerError("");
    try { await post(`/workspaces/${manageWorkspaceID}/copy`, { server_id: serverID, path }); workspaces.reload(); setManageWorkspaceID(null); notify(t("project.workspaceCopyQueued")); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspaceManagerBusy(false); }
  };
  const loadWorkspaceDeletionPlan = async (force: boolean) => {
    if (!manageWorkspaceID) return;
    setWorkspacePlanLoading(true); setWorkspaceManagerError("");
    try { setWorkspaceDeletionPlan(await post<WorkspaceDeletionPlan>(`/workspaces/${manageWorkspaceID}/deletion-plan?force=${force}`, {})); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspacePlanLoading(false); }
  };
  const deleteWorkspace = async (mode: WorkspaceDeletionMode, force: boolean) => {
    if (!manageWorkspaceID) return;
    setWorkspaceManagerBusy(true); setWorkspaceManagerError("");
    try { await remove(`/workspaces/${manageWorkspaceID}?mode=${mode}&force=${force}`); workspaces.reload(); projects.reload(); setManageWorkspaceID(null); notify(t(mode === "files" ? "project.workspaceDeleteQueued" : "project.workspaceRemoved")); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspaceManagerBusy(false); }
  };
  const gitLabels = { title: t("project.gitTitle"), status: t("project.gitStatus"), branches: t("project.gitBranches"), remotes: t("project.gitRemotes"), commits: t("project.gitCommits"), refresh: t("project.gitRefresh"), refreshing: t("project.gitRefreshing"), branch: t("column.branch"), head: t("project.gitHead"), upstream: t("project.gitUpstream"), ahead: t("project.gitAhead"), behind: t("project.gitBehind"), staged: t("project.gitStaged"), unstaged: t("project.gitUnstaged"), untracked: t("project.gitUntracked"), clean: t("project.gitClean"), dirty: t("project.gitDirty"), noBranches: t("project.gitNoBranches"), noRemotes: t("project.gitNoRemotes"), noCommits: t("project.gitNoCommits"), close: t("common.close"), sync: t("project.gitSync"), remote: t("project.gitRemote"), ref: t("project.gitRef"), fetch: t("project.gitFetch"), pull: t("project.gitPull"), push: t("project.gitPush"), setUpstream: t("project.gitSetUpstream"), createBranch: t("project.gitCreateBranch"), branchName: t("project.gitBranchName"), startPoint: t("project.gitStartPoint"), checkout: t("project.gitCheckout"), detach: t("project.gitDetach"), rename: t("common.rename"), edit: t("common.edit"), delete: t("common.delete"), forceDelete: t("project.gitForceDelete"), addRemote: t("project.gitAddRemote"), remoteName: t("project.gitRemoteName"), remoteURL: t("project.remoteURL"), save: t("common.save"), cancel: t("common.cancel"), current: t("project.gitCurrent"), local: t("project.gitLocal"), remoteBranch: t("project.gitRemoteBranch"), actionQueued: t("project.gitActionQueued"), stagedChanges: t("project.gitStagedChanges"), unstagedChanges: t("project.gitUnstagedChanges"), noStagedChanges: t("project.gitNoStagedChanges"), noChanges: t("codex.noChanges"), stage: t("project.gitStage"), stageAll: t("project.gitStageAll"), unstage: t("project.gitUnstage"), unstageAll: t("project.gitUnstageAll"), discard: t("project.gitDiscard"), discardConfirm: t("project.gitDiscardConfirm"), commitMessage: t("project.gitCommitMessage"), commitPlaceholder: t("project.gitCommitPlaceholder"), commitAction: t("project.gitCommitAction"), readOnly: t("project.gitReadOnly"), modified: t("codex.changeModified"), added: t("codex.changeAdded"), deleted: t("codex.changeDeleted"), renamed: t("codex.changeRenamed"), copied: t("codex.changeCopied"), untrackedFile: t("project.gitUntracked"), conflicted: t("codex.changeConflicted") };
  const workspaceManagerLabels = { title: t("project.workspaceManage"), rename: t("common.rename"), move: t("project.workspaceMove"), copy: t("project.workspaceCopy"), delete: t("common.delete"), displayName: t("project.workspaceName"), currentPath: t("project.workspaceCurrentPath"), targetPath: t("project.workspaceTargetPath"), targetServer: t("project.workspaceTargetServer"), sameServer: t("project.workspaceSameServer"), managedOnly: t("project.workspaceManagedOnly"), save: t("common.save"), moving: t("project.workspaceMoving"), copying: t("project.workspaceCopying"), loadingPlan: t("project.deletionLoading"), metadataOnly: t("project.deleteMetadataOnly"), deleteFiles: t("project.workspaceDeleteFiles"), metadataDescription: t("project.workspaceMetadataDescription"), filesDescription: t("project.workspaceFilesDescription"), dirty: t("project.gitDirty"), activeOperations: t("project.deleteActiveOperations"), threads: t("project.workspaceThreads"), childWorkspaces: t("project.workspaceChildren"), force: t("project.workspaceForceDelete"), blockers: t("project.deleteBlockers"), noBlockers: t("project.deleteNoBlockers"), confirmLabel: t("project.deleteConfirmLabel"), confirmPlaceholder: t("project.deleteConfirmPlaceholder"), deleting: t("project.deleting"), cancel: t("common.cancel") };
  const projectDeletionLabels = { title: t("project.deleteTitle"), loading: t("project.deletionLoading"), metadataOnly: t("project.deleteMetadataOnly"), metadataDescription: t("project.deleteMetadataDescription"), managedFiles: t("project.deleteManagedFiles"), managedDescription: t("project.deleteManagedDescription"), workspaces: t("column.workspaces"), managed: t("project.deleteManagedCount"), observed: t("project.deleteObservedCount"), dirty: t("project.gitDirty"), activeOperations: t("project.deleteActiveOperations"), activeTasks: t("project.deleteActiveTasks"), activeDeployments: t("project.deleteActiveDeployments"), remotePreserved: t("project.deleteRemotePreserved"), blockers: t("project.deleteBlockers"), noBlockers: t("project.deleteNoBlockers"), confirmLabel: t("project.deleteConfirmLabel"), confirmPlaceholder: t("project.deleteConfirmPlaceholder"), cancel: t("common.cancel"), deleting: t("project.deleting"), deleteMetadata: t("project.deleteMetadataAction"), deleteFiles: t("project.deleteFilesAction") };
  const managedWorkspace = (workspaces.data ?? []).find(workspace => workspace.id === manageWorkspaceID) ?? null;
  const deletingProject = (projects.data ?? []).find(project => project.id === deleteProjectID) ?? null;
  return <div className="page-stack project-page">
    <Section title={t("project.title")} icon={<GitBranch size={18} />} action={<div className="section-actions"><button className="secondary-button small" disabled={!filtered} onClick={clearFilters}>{t("project.clearFilters")}</button><button className="primary-button" onClick={openDialog}><Plus size={17} />{t("project.createEntry")}</button></div>}>
      <div className="resource-filter-bar"><label><span>{t("project.filterName")}</span><input value={projectName} onChange={event => setProjectName(event.target.value)} placeholder={t("project.filterNamePlaceholder")} /></label><label><span>{t("project.filterServer")}</span><select value={projectServerID} onChange={event => setProjectServerID(event.target.value)}><option value="">{t("project.filterAllServers")}</option>{(servers.data ?? []).map(server => <option key={server.id} value={server.id}>{server.name}</option>)}</select></label><label><span>{t("project.filterStatus")}</span><select value={projectStatus} onChange={event => setProjectStatus(event.target.value)}><option value="">{t("project.filterAllStatuses")}</option><option value="ready">{t("status.ready")}</option><option value="provisioning">{t("status.provisioning")}</option><option value="failed">{t("status.failed")}</option><option value="partial">{t("status.partial")}</option></select></label></div>
      <ProjectTable projects={projects.data ?? []} labels={projectLabels} slots={{ DataTable, Status }} formatTime={relative} formatImportMessage={importMessage} onSelect={project => { setDetailError(""); setDetailProjectID(project.id); }} renderActions={(project, state: ProjectLifecycleState) => {
        const failed = (state === "failed" || state === "partial") && project.workspace_count === 0;
        const action = projectAction?.id === project.id ? projectAction.kind : null;
        const targetServer = (servers.data ?? []).find(server => server.id === project.import_server_id);
        const blankFailure = project.status === "failed" || project.status === "partial";
        const retryAvailable = blankFailure || targetServer?.status === "online";
        return <><button className="icon-button" title={t("project.edit")} onClick={() => { setDetailError(""); setDetailProjectID(project.id); }}><Pencil size={15} /></button>{project.hidden_at && <button className="secondary-button small" disabled={projectAction !== null} onClick={() => void restoreProject(project)}>{action === "restore" ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}{t("project.restore")}</button>}{failed && <button className="icon-button" disabled={projectAction !== null || !retryAvailable} title={retryAvailable ? t(blankFailure ? "project.retryCreate" : "project.retryImport") : t("project.retryOffline")} onClick={() => void retryProject(project)}>{action === "retry" ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button>}<button className="icon-button danger" disabled={projectAction !== null || projectDeletionBusy} title={t("project.deleteTitle")} onClick={() => void openProjectDeletion(project)}><Trash2 size={15} /></button></>;
      }} />
    </Section>
    <Section title={t("project.workspaces")} icon={<Boxes size={18} />}>
      <div className="resource-filter-bar"><label><span>{t("project.filterPath")}</span><input value={workspacePath} onChange={event => setWorkspacePath(event.target.value)} placeholder={t("project.filterPathPlaceholder")} /></label><label><span>{t("project.filterServer")}</span><select value={workspaceServerID} onChange={event => setWorkspaceServerID(event.target.value)}><option value="">{t("project.filterAllServers")}</option>{(servers.data ?? []).map(server => <option key={server.id} value={server.id}>{server.name}</option>)}</select></label><label><span>{t("project.filterGitStatus")}</span><select value={workspaceGitStatus} onChange={event => setWorkspaceGitStatus(event.target.value)}><option value="">{t("project.filterAllStatuses")}</option><option value="clean">{t("project.gitClean")}</option><option value="dirty">{t("project.gitDirty")}</option><option value="error">{t("project.gitStatusError")}</option></select></label></div>
      <WorkspaceTable workspaces={workspaces.data ?? []} labels={workspaceLabels} slots={{ DataTable, Status }} formatCommit={shortSHA} renderActions={workspace => <><button className="icon-button" title={t("project.viewGit")} onClick={() => openWorkspaceGit(workspace.id)}><GitBranch size={15} /></button><button className="icon-button" title={t("project.workspaceManage")} onClick={() => openWorkspaceManager(workspace)}><Settings size={15} /></button></>} />
    </Section>
    <CreateProjectDialog open={dialog} value={form} servers={serverOptions} labels={labels} slots={{ Dialog, Field, DialogActions }} busy={busy} error={createError} onChange={setForm} onClose={close} onSubmit={submit} />
    <ProjectDetailsDialog open={detailProjectID !== null} detail={detail.data} loading={detail.loading} busy={detailBusy} error={detailError || detail.error} labels={detailLabels} slots={{ Dialog, Field, DialogActions }} onClose={() => { if (!detailBusy) setDetailProjectID(null); }} onSubmit={saveProjectDetails} />
    <WorkspaceGitDialog open={gitWorkspaceID !== null} snapshot={gitSnapshot.data} loading={gitSnapshot.loading} busy={gitBusy} writable={(workspaces.data ?? []).find(workspace => workspace.id === gitWorkspaceID)?.management_mode === "managed"} error={gitError} labels={gitLabels} Dialog={Dialog} onClose={() => { if (!gitBusy) setGitWorkspaceID(null); }} onRefresh={() => void refreshWorkspaceGit()} onAction={runGitAction} />
    <WorkspaceManagerDialog open={manageWorkspaceID !== null} workspace={managedWorkspace} servers={servers.data ?? []} plan={workspaceDeletionPlan} planLoading={workspacePlanLoading} busy={workspaceManagerBusy} error={workspaceManagerError} labels={workspaceManagerLabels} Dialog={Dialog} onClose={() => { if (!workspaceManagerBusy) setManageWorkspaceID(null); }} onRename={renameWorkspace} onMove={moveWorkspace} onCopy={copyWorkspace} onLoadDeletionPlan={loadWorkspaceDeletionPlan} onDelete={deleteWorkspace} />
    <ProjectDeletionDialog open={deleteProjectID !== null} project={deletingProject} plan={projectDeletionPlan} loading={projectDeletionBusy && !projectDeletionPlan} busy={projectDeletionBusy} error={projectDeletionError} labels={projectDeletionLabels} Dialog={Dialog} onClose={() => { if (!projectDeletionBusy) setDeleteProjectID(null); }} onSubmit={deleteProject} />
  </div>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}</label>;
}

function Dialog(props: Omit<DialogProps, "closeLabel">) {
  const { t } = useI18n();
  return <AccessibleDialog {...props} closeLabel={t("common.close")} />;
}

function message(error: unknown) {
  return error instanceof Error ? error.message : "Request failed";
}

export default ProjectsPage;
