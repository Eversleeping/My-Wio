import { GitBranch, GitCommit, Globe2, LoaderCircle, RefreshCw } from "lucide-react";
import { useState, type ComponentType } from "react";
import type { WorkspaceGitSnapshot } from "../../types";
import type { DialogSlotProps } from "./slots";

type GitDialogLabels = { title: string; status: string; branches: string; remotes: string; commits: string; refresh: string; refreshing: string; branch: string; head: string; upstream: string; ahead: string; behind: string; staged: string; unstaged: string; untracked: string; clean: string; dirty: string; noBranches: string; noRemotes: string; noCommits: string; close: string };

export function WorkspaceGitDialog({ open, snapshot, loading, busy, labels, Dialog, onClose, onRefresh }: { open: boolean; snapshot: WorkspaceGitSnapshot | null; loading: boolean; busy: boolean; labels: GitDialogLabels; Dialog: ComponentType<DialogSlotProps>; onClose: () => void; onRefresh: () => void }) {
  const [tab, setTab] = useState<"status" | "branches" | "remotes" | "commits">("status");
  const data = snapshot?.data;
  return <Dialog open={open} title={labels.title} onClose={onClose} wide><div className="workspace-git-dialog">
    {loading && <div className="empty-state"><LoaderCircle className="spin" size={18} />...</div>}
    {snapshot?.error && <div className="error-banner">{snapshot.error}</div>}
    {data && <>
      <div className="segmented-control workspace-git-tabs" role="tablist"><button type="button" className={tab === "status" ? "active" : ""} onClick={() => setTab("status")}><GitCommit size={14} />{labels.status}</button><button type="button" className={tab === "branches" ? "active" : ""} onClick={() => setTab("branches")}><GitBranch size={14} />{labels.branches}</button><button type="button" className={tab === "remotes" ? "active" : ""} onClick={() => setTab("remotes")}><Globe2 size={14} />{labels.remotes}</button><button type="button" className={tab === "commits" ? "active" : ""} onClick={() => setTab("commits")}><GitCommit size={14} />{labels.commits}</button></div>
      {tab === "status" && <div className="git-status-grid"><GitStat label={labels.branch} value={data.status.branch || "-"} /><GitStat label={labels.head} value={data.status.head?.slice(0, 12) || (data.status.unborn ? "unborn" : "-")} /><GitStat label={labels.upstream} value={data.status.upstream || "-"} /><GitStat label={labels.ahead} value={String(data.status.ahead)} /><GitStat label={labels.behind} value={String(data.status.behind)} /><GitStat label={labels.staged} value={String(data.status.staged)} /><GitStat label={labels.unstaged} value={String(data.status.unstaged)} /><GitStat label={labels.untracked} value={String(data.status.untracked)} /><div className={`status-tag ${data.status.dirty ? "dirty" : "clean"}`}>{data.status.dirty ? labels.dirty : labels.clean}</div></div>}
      {tab === "branches" && <div className="git-list">{data.branches.length ? data.branches.map(branch => <div className="git-list-row" key={branch.full_name}><GitBranch size={14} /><strong>{branch.name}</strong><code>{branch.commit_sha.slice(0, 10)}</code>{branch.current && <span className="status-tag ready">current</span>}</div>) : <div className="empty-state">{labels.noBranches}</div>}</div>}
      {tab === "remotes" && <div className="git-list">{data.remotes.length ? data.remotes.map(remote => <div className="git-list-row" key={remote.name}><Globe2 size={14} /><strong>{remote.name}</strong><code className="truncate-code">{remote.fetch_urls[0] || "-"}</code></div>) : <div className="empty-state">{labels.noRemotes}</div>}</div>}
      {tab === "commits" && <div className="git-list">{data.commits.length ? data.commits.map(commit => <div className="git-commit-row" key={commit.sha}><div><strong>{commit.title}</strong><code>{commit.sha.slice(0, 12)}</code></div><small>{commit.author_name} · {new Date(commit.authored_at).toLocaleString()}</small></div>) : <div className="empty-state">{labels.noCommits}</div>}</div>}
    </>}
    <div className="dialog-actions"><button type="button" className="secondary-button" onClick={onClose}>{labels.close}</button><button type="button" className="primary-button" disabled={busy} onClick={onRefresh}>{busy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{busy ? labels.refreshing : labels.refresh}</button></div>
  </div></Dialog>;
}

function GitStat({ label, value }: { label: string; value: string }) { return <div className="git-stat"><small>{label}</small><strong>{value}</strong></div>; }
