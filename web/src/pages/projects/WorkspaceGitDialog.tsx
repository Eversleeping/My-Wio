import { ArrowDownToLine, ArrowRightLeft, ArrowUpFromLine, GitBranch, GitCommit, Globe2, LoaderCircle, Pencil, Plus, RefreshCw, Save, Trash2, X } from "lucide-react";
import { useEffect, useState, type ComponentType, type FormEvent } from "react";
import type { WorkspaceGitSnapshot } from "../../types";
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
  | { type: "push"; remote: string; ref: string; setUpstream: boolean };

export interface GitDialogLabels {
  title: string; status: string; branches: string; remotes: string; commits: string; refresh: string; refreshing: string; branch: string; head: string; upstream: string; ahead: string; behind: string; staged: string; unstaged: string; untracked: string; clean: string; dirty: string; noBranches: string; noRemotes: string; noCommits: string; close: string;
  sync: string; remote: string; ref: string; fetch: string; pull: string; push: string; setUpstream: string; createBranch: string; branchName: string; startPoint: string; checkout: string; detach: string; rename: string; edit: string; delete: string; forceDelete: string; addRemote: string; remoteName: string; remoteURL: string; save: string; cancel: string; current: string; local: string; remoteBranch: string; actionQueued: string;
}

export function WorkspaceGitDialog({ open, snapshot, loading, busy, error, labels, Dialog, onClose, onRefresh, onAction }: {
  open: boolean;
  snapshot: WorkspaceGitSnapshot | null;
  loading: boolean;
  busy: boolean;
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
  const data = snapshot?.data;
  useEffect(() => {
    if (!open || !data) return;
    const firstRemote = data.remotes[0]?.name ?? "origin";
    setSyncRemote(current => current || firstRemote);
    setSyncRef(current => current || data.status.branch || "HEAD");
    setCheckoutRef(current => current || data.status.branch || "HEAD");
  }, [open, data?.workspace_id, data?.status.branch, data?.remotes]);
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
  return <Dialog open={open} title={labels.title} onClose={onClose} wide><div className="workspace-git-dialog">
    {loading && <div className="empty-state"><LoaderCircle className="spin" size={18} />...</div>}
    {(snapshot?.error || error) && <div className="error-banner" role="alert">{error || snapshot?.error}</div>}
    {data && <>
      <div className="segmented-control workspace-git-tabs" role="tablist"><button type="button" role="tab" aria-selected={tab === "status"} className={tab === "status" ? "active" : ""} onClick={() => setTab("status")}><GitCommit size={14} />{labels.status}</button><button type="button" role="tab" aria-selected={tab === "branches"} className={tab === "branches" ? "active" : ""} onClick={() => setTab("branches")}><GitBranch size={14} />{labels.branches}</button><button type="button" role="tab" aria-selected={tab === "remotes"} className={tab === "remotes" ? "active" : ""} onClick={() => setTab("remotes")}><Globe2 size={14} />{labels.remotes}</button><button type="button" role="tab" aria-selected={tab === "commits"} className={tab === "commits" ? "active" : ""} onClick={() => setTab("commits")}><GitCommit size={14} />{labels.commits}</button></div>
      {tab === "status" && <div className="git-tab-stack"><div className="git-status-grid"><GitStat label={labels.branch} value={data.status.branch || "-"} /><GitStat label={labels.head} value={data.status.head?.slice(0, 12) || (data.status.unborn ? "unborn" : "-")} /><GitStat label={labels.upstream} value={data.status.upstream || "-"} /><GitStat label={labels.ahead} value={String(data.status.ahead)} /><GitStat label={labels.behind} value={String(data.status.behind)} /><GitStat label={labels.staged} value={String(data.status.staged)} /><GitStat label={labels.unstaged} value={String(data.status.unstaged)} /><GitStat label={labels.untracked} value={String(data.status.untracked)} /><div className={`status-tag ${data.status.dirty ? "dirty" : "clean"}`}>{data.status.dirty ? labels.dirty : labels.clean}</div></div><div className="git-command-bar"><strong>{labels.sync}</strong><select aria-label={labels.remote} value={syncRemote} disabled={busy} onChange={event => setSyncRemote(event.target.value)}>{data.remotes.map(remote => <option key={remote.name} value={remote.name}>{remote.name}</option>)}</select><input aria-label={labels.ref} value={syncRef} disabled={busy} onChange={event => setSyncRef(event.target.value)} /><label className="compact-check"><input type="checkbox" checked={setUpstream} disabled={busy} onChange={event => setSetUpstream(event.target.checked)} />{labels.setUpstream}</label><button type="button" className="secondary-button small" disabled={busy || !syncRemote} onClick={() => void run({ type: "fetch", remote: syncRemote })}><ArrowDownToLine size={14} />{labels.fetch}</button><button type="button" className="secondary-button small" disabled={busy || !syncRemote || !syncRef || data.status.dirty} onClick={() => void run({ type: "pull", remote: syncRemote, branch: syncRef })}><ArrowRightLeft size={14} />{labels.pull}</button><button type="button" className="secondary-button small" disabled={busy || !syncRemote || !syncRef} onClick={() => void run({ type: "push", remote: syncRemote, ref: syncRef, setUpstream })}><ArrowUpFromLine size={14} />{labels.push}</button></div></div>}
      {tab === "branches" && <div className="git-tab-stack"><form className="git-command-bar" onSubmit={createBranch}><strong>{labels.createBranch}</strong><input aria-label={labels.branchName} value={branchName} disabled={busy} onChange={event => setBranchName(event.target.value)} placeholder={labels.branchName} required /><input aria-label={labels.startPoint} value={startPoint} disabled={busy} onChange={event => setStartPoint(event.target.value)} placeholder="HEAD" /><button className="primary-button small" disabled={busy || !branchName.trim()}><Plus size={14} />{labels.createBranch}</button></form><div className="git-command-bar"><strong>{labels.checkout}</strong><input aria-label={labels.ref} value={checkoutRef} disabled={busy} onChange={event => setCheckoutRef(event.target.value)} /><label className="compact-check"><input type="checkbox" checked={detach} disabled={busy} onChange={event => setDetach(event.target.checked)} />{labels.detach}</label><button type="button" className="secondary-button small" disabled={busy || !checkoutRef.trim() || data.status.dirty} onClick={() => void run({ type: "checkout", ref: checkoutRef.trim(), detach })}><ArrowRightLeft size={14} />{labels.checkout}</button><label className="compact-check danger"><input type="checkbox" checked={forceDelete} disabled={busy} onChange={event => setForceDelete(event.target.checked)} />{labels.forceDelete}</label></div><div className="git-list">{data.branches.length ? data.branches.map(branch => <div className="git-list-row git-branch-row" key={branch.full_name}><GitBranch size={14} />{editingBranch === branch.name ? <input value={renamedBranch} disabled={busy} onChange={event => setRenamedBranch(event.target.value)} autoFocus /> : <strong>{branch.name}</strong>}<code>{branch.commit_sha.slice(0, 10)}</code><span className={`status-tag ${branch.current ? "ready" : "neutral"}`}>{branch.current ? labels.current : branch.kind === "local" ? labels.local : labels.remoteBranch}</span><div className="row-actions">{editingBranch === branch.name ? <><button type="button" className="icon-button" title={labels.save} disabled={busy || !renamedBranch.trim()} onClick={() => void run({ type: "branch.rename", branch: branch.name, name: renamedBranch.trim() }, () => setEditingBranch(""))}><Save size={14} /></button><button type="button" className="icon-button" title={labels.cancel} disabled={busy} onClick={() => setEditingBranch("")}><X size={14} /></button></> : <>{!branch.current && <button type="button" className="icon-button" title={labels.checkout} disabled={busy || data.status.dirty} onClick={() => void run({ type: "checkout", ref: branch.name, detach: false })}><ArrowRightLeft size={14} /></button>}{branch.kind === "local" && <button type="button" className="icon-button" title={labels.rename} disabled={busy} onClick={() => { setEditingBranch(branch.name); setRenamedBranch(branch.name); }}><Pencil size={14} /></button>}{branch.kind === "local" && !branch.current && <button type="button" className="icon-button danger" title={labels.delete} disabled={busy} onClick={() => void run({ type: "branch.delete", branch: branch.name, force: forceDelete })}><Trash2 size={14} /></button>}</>}</div></div>) : <div className="empty-state">{labels.noBranches}</div>}</div></div>}
      {tab === "remotes" && <div className="git-tab-stack"><form className="git-command-bar" onSubmit={addRemote}><strong>{labels.addRemote}</strong><input aria-label={labels.remoteName} value={remoteName} disabled={busy} onChange={event => setRemoteName(event.target.value)} placeholder="origin" required /><input className="git-url-input" aria-label={labels.remoteURL} value={remoteURL} disabled={busy} onChange={event => setRemoteURL(event.target.value)} placeholder="https://git.example.com/team/project.git" required /><button className="primary-button small" disabled={busy || !remoteName.trim() || !remoteURL.trim()}><Plus size={14} />{labels.addRemote}</button></form><div className="git-list">{data.remotes.length ? data.remotes.map(remote => <div className="git-list-row git-remote-row" key={remote.name}><Globe2 size={14} /><strong>{remote.name}</strong>{editingRemote === remote.name ? <input value={editedRemoteURL} disabled={busy} onChange={event => setEditedRemoteURL(event.target.value)} autoFocus /> : <code className="truncate-code">{remote.fetch_urls[0] || "-"}</code>}<div className="row-actions">{editingRemote === remote.name ? <><button type="button" className="icon-button" title={labels.save} disabled={busy || !editedRemoteURL.trim()} onClick={() => void run({ type: "remote.update", remote: remote.name, url: editedRemoteURL.trim() }, () => setEditingRemote(""))}><Save size={14} /></button><button type="button" className="icon-button" title={labels.cancel} disabled={busy} onClick={() => setEditingRemote("")}><X size={14} /></button></> : <><button type="button" className="icon-button" title={labels.fetch} disabled={busy} onClick={() => void run({ type: "fetch", remote: remote.name })}><ArrowDownToLine size={14} /></button><button type="button" className="icon-button" title={labels.edit} disabled={busy} onClick={() => { setEditingRemote(remote.name); setEditedRemoteURL(remote.fetch_urls[0] || ""); }}><Pencil size={14} /></button><button type="button" className="icon-button danger" title={labels.delete} disabled={busy} onClick={() => void run({ type: "remote.delete", remote: remote.name })}><Trash2 size={14} /></button></>}</div></div>) : <div className="empty-state">{labels.noRemotes}</div>}</div></div>}
      {tab === "commits" && <div className="git-list">{data.commits.length ? data.commits.map(commit => <div className="git-commit-row" key={commit.sha}><div><strong>{commit.title}</strong><code>{commit.sha.slice(0, 12)}</code></div><small>{commit.author_name} · {new Date(commit.authored_at).toLocaleString()}</small></div>) : <div className="empty-state">{labels.noCommits}</div>}</div>}
    </>}
    <div className="dialog-actions"><button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{labels.close}</button><button type="button" className="primary-button" disabled={busy} onClick={onRefresh}>{busy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{busy ? labels.refreshing : labels.refresh}</button></div>
  </div></Dialog>;
}

function GitStat({ label, value }: { label: string; value: string }) { return <div className="git-stat"><small>{label}</small><strong>{value}</strong></div>; }
