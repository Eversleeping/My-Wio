import { ArrowDownToLine, ArrowRightLeft, ArrowUpFromLine, GitBranch, GitCommit, Globe2, LoaderCircle, Pencil, Plus, RefreshCw, Save, Trash2, X } from "lucide-react";
import { useEffect, useState, type ComponentType, type FormEvent } from "react";
import type { WorkspaceChange, WorkspaceGitSnapshot } from "../../types";
import type { DialogSlotProps } from "./slots";

export type WorkspaceGitAction =
  | { type: "branch.create"; name: string; startPoint: string }
  | { type: "branch.rename"; branch: string; name: string }
  | { type: "branch.delete"; branch: string; force: boolean }
  | { type: "checkout"; ref: string; detach: boolean }
  | { type: "remote.add"; name: string; url: string }
  | { type: "remote.update"; remote: string; url: string }
  | { type: "remote.delete"; remote: string }
  | { type: "fetch"; remote: string }
  | { type: "pull"; remote: string; branch: string }
  | { type: "push"; remote: string; ref: string; setUpstream: boolean }
  | { type: "stage"; paths: string[]; all: boolean }
  | { type: "unstage"; paths: string[]; all: boolean }
  | { type: "discard"; paths: string[]; all: boolean }
  | { type: "commit"; message: string };

export interface GitDialogLabels {
  title: string; status: string; branches: string; remotes: string; commits: string; refresh: string; refreshing: string; branch: string; head: string; upstream: string; ahead: string; behind: string; staged: string; unstaged: string; untracked: string; clean: string; dirty: string; noBranches: string; noRemotes: string; noCommits: string; close: string;
  sync: string; remote: string; ref: string; fetch: string; pull: string; push: string; setUpstream: string; createBranch: string; branchName: string; startPoint: string; checkout: string; detach: string; rename: string; edit: string; delete: string; forceDelete: string; addRemote: string; remoteName: string; remoteURL: string; save: string; cancel: string; current: string; local: string; remoteBranch: string; actionQueued: string;
  stagedChanges: string; unstagedChanges: string; noStagedChanges: string; noChanges: string; stage: string; stageAll: string; unstage: string; unstageAll: string; discard: string; discardConfirm: string; commitMessage: string; commitPlaceholder: string; commitAction: string; readOnly: string;
  modified: string; added: string; deleted: string; renamed: string; copied: string; untrackedFile: string; conflicted: string;
}

type GitData = WorkspaceGitSnapshot["data"];

export function WorkspaceGitDialog({ open, snapshot, loading, busy: requestBusy, writable = true, error, labels, Dialog, onClose, onRefresh, onAction }: {
  open: boolean;
  snapshot: WorkspaceGitSnapshot | null;
  loading: boolean;
  busy: boolean;
  writable?: boolean;
  error: string;
  labels: GitDialogLabels;
  Dialog: ComponentType<DialogSlotProps>;
  onClose: () => void;
  onRefresh: () => void;
  onAction: (action: WorkspaceGitAction) => Promise<void>;
}) {
  const [tab, setTab] = useState<"status" | "branches" | "remotes" | "commits">("status");
  const [branchName, setBranchName] = useState("");
  const [startPoint, setStartPoint] = useState("HEAD");
  const [checkoutRef, setCheckoutRef] = useState("");
  const [detach, setDetach] = useState(false);
  const [forceDelete, setForceDelete] = useState(false);
  const [editingBranch, setEditingBranch] = useState("");
  const [renamedBranch, setRenamedBranch] = useState("");
  const [remoteName, setRemoteName] = useState("");
  const [remoteURL, setRemoteURL] = useState("");
  const [editingRemote, setEditingRemote] = useState("");
  const [editedRemoteURL, setEditedRemoteURL] = useState("");
  const [syncRemote, setSyncRemote] = useState("");
  const [syncRef, setSyncRef] = useState("");
  const [setUpstream, setSetUpstream] = useState(false);
  const [commitMessage, setCommitMessage] = useState("");
  const data = snapshot?.data;
  const branches = data?.branches ?? [];
  const remotes = data?.remotes ?? [];
  const commits = data?.commits ?? [];
  const changes = data?.changes ?? [];
  const refreshing = requestBusy || snapshot?.status === "refreshing";
  const busy = refreshing;
  const writeDisabled = busy || !writable;

  useEffect(() => {
    if (!open || !data) return;
    const firstRemote = data.remotes?.[0]?.name ?? "";
    setSyncRemote(firstRemote);
    setSyncRef(data.status.branch || "HEAD");
    setCheckoutRef(data.status.branch || "HEAD");
  }, [open, data?.workspace_id, data?.status.branch, data?.remotes]);

  useEffect(() => {
    if (open) setCommitMessage("");
  }, [open, data?.workspace_id]);

  const run = async (action: WorkspaceGitAction, after?: () => void) => {
    try {
      await onAction(action);
      after?.();
    } catch {
      // The parent surfaces the API error in the dialog.
    }
  };
  const createBranch = (event: FormEvent) => {
    event.preventDefault();
    if (branchName.trim()) void run({ type: "branch.create", name: branchName.trim(), startPoint: startPoint.trim() || "HEAD" }, () => setBranchName(""));
  };
  const addRemote = (event: FormEvent) => {
    event.preventDefault();
    if (remoteName.trim() && remoteURL.trim()) void run({ type: "remote.add", name: remoteName.trim(), url: remoteURL.trim() }, () => { setRemoteName(""); setRemoteURL(""); });
  };

  return <Dialog open={open} title={labels.title} onClose={onClose} wide className="workspace-git-dialog-shell"><div className="workspace-git-dialog">
    {loading && <div className="empty-state"><LoaderCircle className="spin" size={18} />...</div>}
    {!loading && snapshot?.status === "refreshing" && <div className="git-refresh-state" role="status"><LoaderCircle className="spin" size={16} />{labels.refreshing}</div>}
    {(snapshot?.error || error) && <div className="error-banner" role="alert">{error || snapshot?.error}</div>}
    {!writable && <div className="git-readonly-banner">{labels.readOnly}</div>}
    {data && <>
      <div className="segmented-control workspace-git-tabs" role="tablist">
        <button type="button" role="tab" aria-selected={tab === "status"} className={tab === "status" ? "active" : ""} onClick={() => setTab("status")}><GitCommit size={14} />{labels.status}</button>
        <button type="button" role="tab" aria-selected={tab === "branches"} className={tab === "branches" ? "active" : ""} onClick={() => setTab("branches")}><GitBranch size={14} />{labels.branches}</button>
        <button type="button" role="tab" aria-selected={tab === "remotes"} className={tab === "remotes" ? "active" : ""} onClick={() => setTab("remotes")}><Globe2 size={14} />{labels.remotes}</button>
        <button type="button" role="tab" aria-selected={tab === "commits"} className={tab === "commits" ? "active" : ""} onClick={() => setTab("commits")}><GitCommit size={14} />{labels.commits}</button>
      </div>
      <div className="workspace-git-tab-panel" role="tabpanel" tabIndex={0}>
        {tab === "status" && <GitStatusWorkspace data={data} changes={changes} remotes={remotes} busy={busy} writable={writable} labels={labels} commitMessage={commitMessage} syncRemote={syncRemote} syncRef={syncRef} setUpstream={setUpstream} onCommitMessage={setCommitMessage} onSyncRemote={setSyncRemote} onSyncRef={setSyncRef} onSetUpstream={setSetUpstream} onRun={run} />}
        {tab === "branches" && <div className="git-tab-stack"><form className="git-command-bar" onSubmit={createBranch}><strong>{labels.createBranch}</strong><input aria-label={labels.branchName} value={branchName} disabled={writeDisabled} onChange={event => setBranchName(event.target.value)} placeholder={labels.branchName} required /><input aria-label={labels.startPoint} value={startPoint} disabled={writeDisabled} onChange={event => setStartPoint(event.target.value)} placeholder="HEAD" /><button className="primary-button small" disabled={writeDisabled || !branchName.trim()}><Plus size={14} />{labels.createBranch}</button></form><div className="git-command-bar"><strong>{labels.checkout}</strong><input aria-label={labels.ref} value={checkoutRef} disabled={writeDisabled} onChange={event => setCheckoutRef(event.target.value)} /><label className="compact-check"><input type="checkbox" checked={detach} disabled={writeDisabled} onChange={event => setDetach(event.target.checked)} />{labels.detach}</label><button type="button" className="secondary-button small" disabled={writeDisabled || !checkoutRef.trim() || data.status.dirty} onClick={() => void run({ type: "checkout", ref: checkoutRef.trim(), detach })}><ArrowRightLeft size={14} />{labels.checkout}</button><label className="compact-check danger"><input type="checkbox" checked={forceDelete} disabled={writeDisabled} onChange={event => setForceDelete(event.target.checked)} />{labels.forceDelete}</label></div><div className="git-list">{branches.length ? branches.map(branch => <div className="git-list-row git-branch-row" key={branch.full_name}><GitBranch size={14} />{editingBranch === branch.name ? <input value={renamedBranch} disabled={writeDisabled} onChange={event => setRenamedBranch(event.target.value)} autoFocus /> : <strong>{branch.name}</strong>}<code>{branch.commit_sha.slice(0, 10)}</code><span className={`status-tag ${branch.current ? "ready" : "neutral"}`}>{branch.current ? labels.current : branch.kind === "local" ? labels.local : labels.remoteBranch}</span><div className="row-actions">{editingBranch === branch.name ? <><button type="button" className="icon-button" title={labels.save} disabled={writeDisabled || !renamedBranch.trim()} onClick={() => void run({ type: "branch.rename", branch: branch.name, name: renamedBranch.trim() }, () => setEditingBranch(""))}><Save size={14} /></button><button type="button" className="icon-button" title={labels.cancel} disabled={writeDisabled} onClick={() => setEditingBranch("")}><X size={14} /></button></> : <>{!branch.current && <button type="button" className="icon-button" title={labels.checkout} disabled={writeDisabled || data.status.dirty} onClick={() => void run({ type: "checkout", ref: branch.name, detach: false })}><ArrowRightLeft size={14} /></button>}{branch.kind === "local" && <button type="button" className="icon-button" title={labels.rename} disabled={writeDisabled} onClick={() => { setEditingBranch(branch.name); setRenamedBranch(branch.name); }}><Pencil size={14} /></button>}{branch.kind === "local" && !branch.current && <button type="button" className="icon-button danger" title={labels.delete} disabled={writeDisabled} onClick={() => void run({ type: "branch.delete", branch: branch.name, force: forceDelete })}><Trash2 size={14} /></button>}</>}</div></div>) : <div className="empty-state">{refreshing ? labels.refreshing : labels.noBranches}</div>}</div></div>}
        {tab === "remotes" && <div className="git-tab-stack"><form className="git-command-bar" onSubmit={addRemote}><strong>{labels.addRemote}</strong><input aria-label={labels.remoteName} value={remoteName} disabled={writeDisabled} onChange={event => setRemoteName(event.target.value)} placeholder="origin" required /><input className="git-url-input" aria-label={labels.remoteURL} value={remoteURL} disabled={writeDisabled} onChange={event => setRemoteURL(event.target.value)} placeholder="https://git.example.com/team/project.git" required /><button className="primary-button small" disabled={writeDisabled || !remoteName.trim() || !remoteURL.trim()}><Plus size={14} />{labels.addRemote}</button></form><div className="git-list">{remotes.length ? remotes.map(remote => { const fetchURL = remote.fetch_urls?.[0] || "-"; return <div className="git-list-row git-remote-row" key={remote.name}><Globe2 size={14} /><strong>{remote.name}</strong>{editingRemote === remote.name ? <input value={editedRemoteURL} disabled={writeDisabled} onChange={event => setEditedRemoteURL(event.target.value)} autoFocus /> : <code className="truncate-code" title={fetchURL}>{fetchURL}</code>}<div className="row-actions">{editingRemote === remote.name ? <><button type="button" className="icon-button" title={labels.save} disabled={writeDisabled || !editedRemoteURL.trim()} onClick={() => void run({ type: "remote.update", remote: remote.name, url: editedRemoteURL.trim() }, () => setEditingRemote(""))}><Save size={14} /></button><button type="button" className="icon-button" title={labels.cancel} disabled={writeDisabled} onClick={() => setEditingRemote("")}><X size={14} /></button></> : <><button type="button" className="icon-button" title={labels.fetch} disabled={writeDisabled} onClick={() => void run({ type: "fetch", remote: remote.name })}><ArrowDownToLine size={14} /></button><button type="button" className="icon-button" title={labels.edit} disabled={writeDisabled} onClick={() => { setEditingRemote(remote.name); setEditedRemoteURL(remote.fetch_urls?.[0] || ""); }}><Pencil size={14} /></button><button type="button" className="icon-button danger" title={labels.delete} disabled={writeDisabled} onClick={() => void run({ type: "remote.delete", remote: remote.name })}><Trash2 size={14} /></button></>}</div></div>; }) : <div className="empty-state">{refreshing ? labels.refreshing : labels.noRemotes}</div>}</div></div>}
        {tab === "commits" && <div className="git-list">{commits.length ? commits.map(commit => <div className="git-commit-row" key={commit.sha}><div><strong>{commit.title}</strong><code>{commit.sha.slice(0, 12)}</code></div><small>{commit.author_name} · {new Date(commit.authored_at).toLocaleString()}</small></div>) : <div className="empty-state">{refreshing ? labels.refreshing : labels.noCommits}</div>}</div>}
      </div>
    </>}
    <div className="dialog-actions"><button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{labels.close}</button><button type="button" className="primary-button" disabled={refreshing} onClick={onRefresh}>{refreshing ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{refreshing ? labels.refreshing : labels.refresh}</button></div>
  </div></Dialog>;
}

function GitStatusWorkspace({ data, changes, remotes, busy, writable, labels, commitMessage, syncRemote, syncRef, setUpstream, onCommitMessage, onSyncRemote, onSyncRef, onSetUpstream, onRun }: {
  data: GitData;
  changes: WorkspaceChange[];
  remotes: NonNullable<GitData["remotes"]>;
  busy: boolean;
  writable: boolean;
  labels: GitDialogLabels;
  commitMessage: string;
  syncRemote: string;
  syncRef: string;
  setUpstream: boolean;
  onCommitMessage: (value: string) => void;
  onSyncRemote: (value: string) => void;
  onSyncRef: (value: string) => void;
  onSetUpstream: (value: boolean) => void;
  onRun: (action: WorkspaceGitAction, after?: () => void) => Promise<void>;
}) {
  const stagedChanges = changes.filter(change => change.staged);
  const unstagedChanges = changes.filter(change => change.unstaged || change.status === "untracked");
  const writeDisabled = busy || !writable;
  const submitCommit = (event: FormEvent) => {
    event.preventDefault();
    const message = commitMessage.trim();
    if (message && stagedChanges.length) void onRun({ type: "commit", message }, () => onCommitMessage(""));
  };
  const discard = (path: string) => {
    if (window.confirm(`${labels.discardConfirm}\n${path}`)) void onRun({ type: "discard", paths: [path], all: false });
  };

  return <div className="git-status-workspace">
    <div className="git-changes-column">
      <form className="git-commit-composer" onSubmit={submitCommit}>
        <textarea aria-label={labels.commitMessage} value={commitMessage} disabled={writeDisabled} rows={3} maxLength={20 * 1024} placeholder={labels.commitPlaceholder} onChange={event => onCommitMessage(event.target.value)} />
        <div><span className="git-staged-summary">{labels.stagedChanges} <strong>{stagedChanges.length}</strong></span><button className="primary-button" disabled={writeDisabled || stagedChanges.length === 0 || !commitMessage.trim()}><GitCommit size={16} />{labels.commitAction}</button></div>
      </form>
      <GitChangeSection title={labels.stagedChanges} empty={labels.noStagedChanges} changes={stagedChanges} mode="staged" disabled={writeDisabled} labels={labels} onAll={() => void onRun({ type: "unstage", paths: [], all: true })} onStage={path => void onRun({ type: "stage", paths: [path], all: false })} onUnstage={path => void onRun({ type: "unstage", paths: [path], all: false })} onDiscard={discard} />
      <GitChangeSection title={labels.unstagedChanges} empty={labels.noChanges} changes={unstagedChanges} mode="unstaged" disabled={writeDisabled} labels={labels} onAll={() => void onRun({ type: "stage", paths: [], all: true })} onStage={path => void onRun({ type: "stage", paths: [path], all: false })} onUnstage={path => void onRun({ type: "unstage", paths: [path], all: false })} onDiscard={discard} />
    </div>
    <aside className="git-repository-column">
      <div className="git-repository-heading"><div><GitBranch size={17} /><span><strong>{data.status.branch || "-"}</strong><small>{data.status.upstream || labels.upstream}</small></span></div><span className={`status-tag ${data.status.dirty ? "dirty" : "clean"}`}>{data.status.dirty ? labels.dirty : labels.clean}</span></div>
      <dl className="git-repository-facts">
        <GitFact label={labels.head} value={data.status.head?.slice(0, 12) || (data.status.unborn ? "unborn" : "-")} />
        <GitFact label={labels.ahead} value={String(data.status.ahead)} />
        <GitFact label={labels.behind} value={String(data.status.behind)} />
        <GitFact label={labels.untracked} value={String(data.status.untracked)} />
      </dl>
      <div className="git-sync-panel"><strong>{labels.sync}</strong><label><span>{labels.remote}</span><select aria-label={labels.remote} value={syncRemote} disabled={writeDisabled} onChange={event => onSyncRemote(event.target.value)}>{remotes.map(remote => <option key={remote.name} value={remote.name}>{remote.name}</option>)}</select></label><label><span>{labels.ref}</span><input aria-label={labels.ref} value={syncRef} disabled={writeDisabled} onChange={event => onSyncRef(event.target.value)} /></label><label className="compact-check"><input type="checkbox" checked={setUpstream} disabled={writeDisabled} onChange={event => onSetUpstream(event.target.checked)} />{labels.setUpstream}</label><div className="git-sync-actions"><button type="button" className="secondary-button small" disabled={writeDisabled || !syncRemote} onClick={() => void onRun({ type: "fetch", remote: syncRemote })}><ArrowDownToLine size={14} />{labels.fetch}</button><button type="button" className="secondary-button small" disabled={writeDisabled || !syncRemote || !syncRef || data.status.dirty} onClick={() => void onRun({ type: "pull", remote: syncRemote, branch: syncRef })}><ArrowRightLeft size={14} />{labels.pull}</button><button type="button" className="secondary-button small" disabled={writeDisabled || !syncRemote || !syncRef} onClick={() => void onRun({ type: "push", remote: syncRemote, ref: syncRef, setUpstream })}><ArrowUpFromLine size={14} />{labels.push}</button></div></div>
    </aside>
  </div>;
}

function GitChangeSection({ title, empty, changes, mode, disabled, labels, onAll, onStage, onUnstage, onDiscard }: {
  title: string;
  empty: string;
  changes: WorkspaceChange[];
  mode: "staged" | "unstaged";
  disabled: boolean;
  labels: GitDialogLabels;
  onAll: () => void;
  onStage: (path: string) => void;
  onUnstage: (path: string) => void;
  onDiscard: (path: string) => void;
}) {
  const allLabel = mode === "staged" ? labels.unstageAll : labels.stageAll;
  return <section className="git-change-section"><header><div><strong>{title}</strong><span>{changes.length}</span></div>{changes.length > 0 && <button type="button" className="icon-button" title={allLabel} aria-label={allLabel} disabled={disabled} onClick={onAll}>{mode === "staged" ? <ArrowUpFromLine size={15} /> : <Plus size={15} />}</button>}</header>{changes.length ? <div className="git-change-list">{changes.map(change => <GitChangeRow key={`${mode}:${change.path}`} change={change} mode={mode} disabled={disabled} labels={labels} onStage={onStage} onUnstage={onUnstage} onDiscard={onDiscard} />)}</div> : <div className="git-change-empty">{empty}</div>}</section>;
}

function GitChangeRow({ change, mode, disabled, labels, onStage, onUnstage, onDiscard }: {
  change: WorkspaceChange;
  mode: "staged" | "unstaged";
  disabled: boolean;
  labels: GitDialogLabels;
  onStage: (path: string) => void;
  onUnstage: (path: string) => void;
  onDiscard: (path: string) => void;
}) {
  const name = change.path.split("/").pop() || change.path;
  const detail = change.old_path ? `${change.old_path} -> ${change.path}` : change.path;
  return <div className="git-change-row" title={detail}><span className={`git-change-status ${change.status}`}>{changeStatusCodes[change.status] ?? "M"}</span><span className="git-change-path"><strong>{name}</strong><small>{detail}</small></span><span className="git-change-label">{changeStatusLabel(change.status, labels)}</span><div className="row-actions">{mode === "staged" ? <button type="button" className="icon-button" title={labels.unstage} aria-label={`${labels.unstage}: ${change.path}`} disabled={disabled} onClick={() => onUnstage(change.path)}><ArrowUpFromLine size={14} /></button> : <><button type="button" className="icon-button" title={labels.stage} aria-label={`${labels.stage}: ${change.path}`} disabled={disabled} onClick={() => onStage(change.path)}><Plus size={14} /></button>{change.status !== "conflicted" && <button type="button" className="icon-button danger" title={labels.discard} aria-label={`${labels.discard}: ${change.path}`} disabled={disabled} onClick={() => onDiscard(change.path)}><Trash2 size={14} /></button>}</>}</div></div>;
}

const changeStatusCodes: Record<string, string> = { modified: "M", added: "A", deleted: "D", renamed: "R", copied: "C", untracked: "?", conflicted: "!" };

function changeStatusLabel(status: string, labels: GitDialogLabels) {
  const values: Record<string, string> = { modified: labels.modified, added: labels.added, deleted: labels.deleted, renamed: labels.renamed, copied: labels.copied, untracked: labels.untrackedFile, conflicted: labels.conflicted };
  return values[status] ?? labels.modified;
}

function GitFact({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
