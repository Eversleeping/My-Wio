import { type FormEvent, type ReactNode, useState } from "react";
import {
  AlertTriangle,
  Check,
  ExternalLink,
  GitBranch,
  History,
  LoaderCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Rocket,
  Server as ServerIcon,
  ShieldCheck,
  Square,
  SquareTerminal,
  Trash2,
  Undo2
} from "lucide-react";
import { post, put, remove } from "../api";
import { ContextMenu, type ContextMenuAction } from "../ContextMenu";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Dialog as AccessibleDialog, DialogActions, type DialogProps } from "../components/Dialog";
import { DataTable, Empty, Section, Status } from "../components/PageUI";
import { formatTime, relative, shortSHA } from "../format";
import { useI18n } from "../i18n";
import type { Deployment, DeploymentDetail, DeploymentTarget, SecretSet, Server, Workspace } from "../types";
import { useData } from "../useData";

export interface PageProps {
  realtime: number;
  notify: (text: string) => void;
}

type DeploymentContainerAction = "start" | "stop" | "restart" | "remove";

function message(error: unknown) {
  return error instanceof Error ? error.message : "Request failed";
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}</label>;
}

function Dialog(props: Omit<DialogProps, "closeLabel">) {
  const { t } = useI18n();
  return <AccessibleDialog {...props} closeLabel={t("common.close")} />;
}

function ErrorBanner({ text }: { text: string }) {
  return <div className="error-banner"><AlertTriangle size={16} />{text}</div>;
}

export function DeploymentsPage({ realtime, notify }: PageProps) {
  const { t } = useI18n();
  const targets = useData<DeploymentTarget[]>("/deployment-targets", realtime);
  const deployments = useData<Deployment[]>("/deployments", realtime);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const servers = useData<Server[]>("/servers", realtime);
  const secrets = useData<SecretSet[]>("/secret-sets", realtime);
  const emptyForm = { source_type: "workspace" as "workspace" | "remote", workspace_id: "", server_id: "", secret_set_id: "", environment: "production", repository: "", git_ref: "", compose_file: "compose.yaml", build_mode: "build", public_url: "", health: "" };
  const [targetDialog, setTargetDialog] = useState(false);
  const [editingTarget, setEditingTarget] = useState<DeploymentTarget | null>(null);
  const [form, setForm] = useState(emptyForm);
  const [busy, setBusy] = useState("");
  const [detailID, setDetailID] = useState("");
  const [deploymentConfirmation, setDeploymentConfirmation] = useState<
    | { type: "target"; target: DeploymentTarget }
    | { type: "history"; deployment: Deployment }
    | { type: "rollback"; target: DeploymentTarget }
    | { type: "container"; target: DeploymentTarget; action: Exclude<DeploymentContainerAction, "start"> }
    | null
  >(null);
  const detail = useData<DeploymentDetail>(detailID ? `/deployments/${detailID}` : null, realtime);
  const active = (status: string) => ["queued", "preparing", "running"].includes(status);
  const openCreate = () => { setEditingTarget(null); setForm(emptyForm); setTargetDialog(true); };
  const openEdit = (target: DeploymentTarget) => {
    let health = "";
    try { health = (JSON.parse(target.health_checks) as Array<{ address?: string }>).map(check => check.address ?? "").filter(Boolean).join("\n"); } catch { health = ""; }
    setEditingTarget(target);
    setForm({ source_type: target.source_type || "remote", workspace_id: target.workspace_id || "", server_id: target.server_id, secret_set_id: target.secret_set_id, environment: target.environment, repository: target.repository, git_ref: target.git_ref, compose_file: target.compose_file, build_mode: target.build_mode, public_url: target.configured_public_url || "", health });
    setTargetDialog(true);
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const { health, ...target } = form;
    const health_checks = health.split(/\r?\n/).map(address => address.trim()).filter(Boolean).map(address => ({ type: address.startsWith("http") ? "http" : "tcp", address, timeout_seconds: 60 }));
    setBusy(editingTarget ? `edit:${editingTarget.id}` : "create");
    try {
      if (editingTarget) await put(`/deployment-targets/${editingTarget.id}`, { ...target, health_checks });
      else await post("/deployment-targets", { ...target, health_checks });
      setTargetDialog(false);
      targets.reload();
      notify(t(editingTarget ? "deployment.targetUpdated" : "deployment.targetCreated"));
    } catch (err) { notify(message(err)); } finally { setBusy(""); }
  };
  const run = async (target: DeploymentTarget) => { setBusy(`deploy:${target.id}`); try { const response = await post<{ deployment: Deployment }>(`/deployment-targets/${target.id}/deploy`, { commit_ref: target.git_ref }); targets.reload(); deployments.reload(); setDetailID(response.deployment.id); notify(t("deployment.queued")); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const rollback = async (target: DeploymentTarget) => { setBusy(`rollback:${target.id}`); try { const response = await post<{ deployment: Deployment }>(`/deployment-targets/${target.id}/rollback`, {}); targets.reload(); deployments.reload(); setDetailID(response.deployment.id); notify(t("deployment.rollbackQueued")); setDeploymentConfirmation(null); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const manageContainer = async (target: DeploymentTarget, action: DeploymentContainerAction) => {
    setBusy(`container:${action}:${target.id}`);
    try {
      await post(`/deployment-targets/${target.id}/container`, { action });
      targets.reload();
      notify(t(`deployment.container${action[0].toUpperCase()}${action.slice(1)}Queued`));
      if (action !== "start") setDeploymentConfirmation(null);
    } catch (err) { notify(message(err)); } finally { setBusy(""); }
  };
  const requestContainerAction = (target: DeploymentTarget, action: DeploymentContainerAction) => {
    if (action === "start") void manageContainer(target, action);
    else setDeploymentConfirmation({ type: "container", target, action });
  };
  const deleteTarget = async (target: DeploymentTarget) => { setBusy(`delete-target:${target.id}`); try { await remove(`/deployment-targets/${target.id}`); targets.reload(); deployments.reload(); notify(t("deployment.targetDeleteQueued")); setDeploymentConfirmation(null); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const deleteHistory = async (item: Deployment) => { setBusy(`delete-deployment:${item.id}`); try { await remove(`/deployments/${item.id}`); if (detailID === item.id) setDetailID(""); deployments.reload(); notify(t("deployment.historyDeleted")); setDeploymentConfirmation(null); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const availableWorkspaces = (workspaces.data ?? []).filter(item => item.server_id === form.server_id && item.status === "ready");
  return <div className="page-stack deployment-page"><Section title={t("deployment.targets")} icon={<Rocket size={18} />} action={<button className="primary-button" onClick={openCreate}><Plus size={17} />{t("deployment.newTarget")}</button>}><DataTable headers={[t("column.project"), t("column.environment"), t("column.server"), t("deployment.containerState"), t("column.gitRef"), t("column.compose"), t("deployment.publicAccess"), t("common.actions")]} empty={t("deployment.noTargets")}>{(targets.data ?? []).map(target => {
    const sourcePath = target.source_type === "workspace" ? target.workspace_path : target.repository;
    const containerStatus = target.container_status || "unknown";
    const serverOnline = (servers.data ?? []).some(server => server.id === target.server_id && server.status === "online");
    const locked = busy !== "" || deploymentConfirmation !== null || containerStatus === "pending" || !serverOnline;
    const primaryContainerAction: DeploymentContainerAction = containerStatus === "running" ? "stop" : "start";
    const primaryContainerLabel = t(primaryContainerAction === "stop" ? "deployment.containerStop" : "deployment.containerStart");
    const actions: ContextMenuAction[] = [
      { id: "rollback", label: t("deployment.rollback"), icon: Undo2, disabled: locked, onSelect: () => setDeploymentConfirmation({ type: "rollback", target }) },
      { id: "edit", label: t("deployment.editTarget"), icon: Pencil, disabled: busy !== "", onSelect: () => openEdit(target) },
      { id: "delete-target", label: t("deployment.deleteTarget"), icon: Trash2, danger: true, separatorBefore: true, disabled: busy !== "" || containerStatus === "pending", onSelect: () => setDeploymentConfirmation({ type: "target", target }) }
    ];
    return <tr key={target.id}><td><div className="cell-main"><strong>{target.project_name}</strong><small title={sourcePath}>{sourcePath}</small></div></td><td><Status value={target.environment} /></td><td>{target.server_name}</td><td><div className="cell-main container-state-cell"><Status value={containerStatus} />{target.container_message && <small title={target.container_message}>{target.container_message}</small>}</div></td><td><code>{target.git_ref}</code></td><td><code>{target.compose_file}</code></td><td><DeploymentPublicLink url={target.public_url} /></td><td><div className="row-actions deployment-target-actions"><button type="button" className="primary-button small" disabled={locked} onClick={() => void run(target)}>{busy === `deploy:${target.id}` ? <LoaderCircle className="spin" size={14} /> : <Rocket size={14} />}{t("deployment.deploy")}</button><button type="button" className="secondary-button small deployment-lifecycle-button" disabled={locked} title={primaryContainerLabel} onClick={() => requestContainerAction(target, primaryContainerAction)}>{busy === `container:${primaryContainerAction}:${target.id}` ? <LoaderCircle className="spin" size={14} /> : primaryContainerAction === "stop" ? <Square size={14} /> : <Play size={14} />}{primaryContainerLabel}</button><button type="button" className="icon-button" disabled={locked || containerStatus === "stopped" || containerStatus === "removed"} title={t("deployment.containerRestart")} aria-label={t("deployment.containerRestart")} onClick={() => requestContainerAction(target, "restart")}>{busy === `container:restart:${target.id}` ? <LoaderCircle className="spin" size={14} /> : <RefreshCw size={14} />}</button><button type="button" className="icon-button danger" disabled={locked || containerStatus === "removed"} title={t("deployment.containerRemove")} aria-label={t("deployment.containerRemove")} onClick={() => requestContainerAction(target, "remove")}>{busy === `container:remove:${target.id}` ? <LoaderCircle className="spin" size={14} /> : <Trash2 size={14} />}</button><ContextMenu label={t("deployment.actions")} actions={actions}>{null}</ContextMenu></div></td></tr>;
  })}</DataTable></Section><Section title={t("deployment.history")} icon={<History size={18} />} action={<button className="icon-button" title={t("common.refresh")} onClick={deployments.reload}><RefreshCw size={16} /></button>}><DataTable headers={[t("column.project"), t("column.environment"), t("column.commit"), t("column.status"), t("deployment.duration"), t("column.message"), t("column.created"), t("deployment.publicAccess"), t("common.actions")]} empty={t("deployment.noHistory")}>{(deployments.data ?? []).map(item => <tr key={item.id}><td><strong>{item.project_name}</strong></td><td>{item.environment}</td><td><code>{shortSHA(item.resolved_commit || item.commit_ref)}</code></td><td><Status value={item.status} /></td><td className="deployment-duration">{deploymentDuration(item)}</td><td className="message-cell" title={item.message}>{item.message || "-"}</td><td>{relative(item.created_at)}</td><td>{item.status === "succeeded" ? <DeploymentPublicLink url={item.public_url} /> : <span className="muted">-</span>}</td><td><div className="row-actions"><button className="icon-button" title={t("deployment.viewLogs")} onClick={() => setDetailID(item.id)}><SquareTerminal size={15} /></button><button className="icon-button danger" disabled={active(item.status) || busy !== ""} title={active(item.status) ? t("deployment.activeCannotDelete") : t("deployment.deleteHistory")} onClick={() => setDeploymentConfirmation({ type: "history", deployment: item })}><Trash2 size={15} /></button></div></td></tr>)}</DataTable></Section>
  {deploymentConfirmation?.type === "target" ? <ConfirmDialog open danger title={t("deployment.deleteTarget")} impact={t("deployment.confirmDeleteTarget", { project: deploymentConfirmation.target.project_name, environment: deploymentConfirmation.target.environment })} confirmLabel={t("deployment.deleteTarget")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy !== ""} onClose={() => setDeploymentConfirmation(null)} onConfirm={() => deleteTarget(deploymentConfirmation.target)} /> : deploymentConfirmation?.type === "history" ? <ConfirmDialog open danger title={t("deployment.deleteHistory")} impact={t("deployment.confirmDeleteHistory")} confirmLabel={t("deployment.deleteHistory")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy !== ""} onClose={() => setDeploymentConfirmation(null)} onConfirm={() => deleteHistory(deploymentConfirmation.deployment)} /> : deploymentConfirmation?.type === "rollback" ? <ConfirmDialog open danger title={t("deployment.rollback")} impact={t("deployment.confirmRollback", { project: deploymentConfirmation.target.project_name, environment: deploymentConfirmation.target.environment })} confirmLabel={t("deployment.rollback")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy !== ""} onClose={() => setDeploymentConfirmation(null)} onConfirm={() => rollback(deploymentConfirmation.target)} /> : deploymentConfirmation?.type === "container" ? <ConfirmDialog open danger title={t(deploymentConfirmation.action === "stop" ? "deployment.containerStop" : deploymentConfirmation.action === "restart" ? "deployment.containerRestart" : "deployment.containerRemove")} impact={t(deploymentConfirmation.action === "stop" ? "deployment.confirmContainerStop" : deploymentConfirmation.action === "restart" ? "deployment.confirmContainerRestart" : "deployment.confirmContainerRemove", { project: deploymentConfirmation.target.project_name, environment: deploymentConfirmation.target.environment })} confirmLabel={t(deploymentConfirmation.action === "stop" ? "deployment.containerStop" : deploymentConfirmation.action === "restart" ? "deployment.containerRestart" : "deployment.containerRemove")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy !== ""} onClose={() => setDeploymentConfirmation(null)} onConfirm={() => manageContainer(deploymentConfirmation.target, deploymentConfirmation.action)} /> : null}
  <Dialog open={targetDialog} title={t(editingTarget ? "deployment.editTargetTitle" : "deployment.targetTitle")} onClose={() => setTargetDialog(false)} wide><form onSubmit={submit}><div className="segmented-control deployment-source-control" role="group" aria-label={t("deployment.sourceType")}><button type="button" className={form.source_type === "workspace" ? "active" : ""} onClick={() => setForm({ ...form, source_type: "workspace", repository: "", git_ref: "", workspace_id: "" })}><ServerIcon size={15} />{t("deployment.sourceWorkspace")}</button><button type="button" className={form.source_type === "remote" ? "active" : ""} onClick={() => setForm({ ...form, source_type: "remote", workspace_id: "", git_ref: form.git_ref || "main" })}><GitBranch size={15} />{t("deployment.sourceRemote")}</button></div><div className="form-grid"><Field label={t("column.server")}><select value={form.server_id} onChange={e => setForm({ ...form, server_id: e.target.value, workspace_id: "" })} required><option value="">{t("deployment.selectServer")}</option>{(servers.data ?? []).filter(item => item.status === "online").map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>{form.source_type === "workspace" ? <Field label={t("deployment.workspace")}><select value={form.workspace_id} onChange={e => { const workspace = availableWorkspaces.find(item => item.id === e.target.value); setForm({ ...form, workspace_id: e.target.value, git_ref: workspace?.branch || "" }); }} required><option value="">{t("deployment.selectWorkspace")}</option>{availableWorkspaces.map(item => <option key={item.id} value={item.id}>{item.project_name} · {item.display_name || item.path}</option>)}</select></Field> : <Field label={t("deployment.repository")}><input value={form.repository} onChange={e => setForm({ ...form, repository: e.target.value })} placeholder="https://example.com/team/project.git" required /></Field>}<Field label={t("column.environment")}><input value={form.environment} onChange={e => setForm({ ...form, environment: e.target.value })} required /></Field><Field label={t("deployment.secretSet")}><select value={form.secret_set_id} onChange={e => setForm({ ...form, secret_set_id: e.target.value })}><option value="">{t("common.none")}</option>{(secrets.data ?? []).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field></div><div className="form-grid thirds"><Field label={t("column.gitRef")}><input value={form.git_ref} onChange={e => setForm({ ...form, git_ref: e.target.value })} placeholder={form.source_type === "workspace" ? t("deployment.currentBranch") : "main"} /></Field><Field label={t("deployment.composeFile")}><input value={form.compose_file} onChange={e => setForm({ ...form, compose_file: e.target.value })} /></Field><Field label={t("deployment.buildMode")}><select value={form.build_mode} onChange={e => setForm({ ...form, build_mode: e.target.value })}><option value="build">{t("deployment.build")}</option><option value="pull">{t("deployment.pull")}</option></select></Field></div><Field label={t("deployment.publicURL")}><input type="text" inputMode="url" autoComplete="url" value={form.public_url} onChange={e => setForm({ ...form, public_url: e.target.value })} placeholder={t("deployment.publicURLPlaceholder")} /></Field><Field label={t("deployment.healthCheck")}><textarea rows={2} value={form.health} onChange={e => setForm({ ...form, health: e.target.value })} placeholder={t("deployment.healthPlaceholder")} /></Field><div className="deployment-preflight-note"><ShieldCheck size={17} /><span>{t("deployment.preflightNote")}</span></div><DialogActions><button type="button" className="secondary-button" onClick={() => setTargetDialog(false)}>{t("common.cancel")}</button><button className="primary-button" disabled={busy !== ""}>{busy ? <LoaderCircle className="spin" size={16} /> : editingTarget ? <Check size={16} /> : <Rocket size={16} />}{t(editingTarget ? "deployment.saveTarget" : "deployment.createTarget")}</button></DialogActions></form></Dialog>
  <Dialog open={Boolean(detailID)} title={t("deployment.logTitle")} onClose={() => setDetailID("")} wide className="deployment-log-dialog"><div className="deployment-log-content">{detail.loading && <div className="deployment-log-loading"><LoaderCircle className="spin" size={20} />{t("common.loading")}</div>}{detail.error && <ErrorBanner text={detail.error} />}{detail.data && <><div className="deployment-log-summary"><div><small>{t("column.project")}</small><strong>{detail.data.deployment.project_name}</strong></div><div><small>{t("column.environment")}</small><Status value={detail.data.deployment.environment} /></div><div><small>{t("column.commit")}</small><code>{shortSHA(detail.data.deployment.resolved_commit || detail.data.deployment.commit_ref)}</code></div><div><small>{t("deployment.duration")}</small><strong>{deploymentDuration(detail.data.deployment)}</strong></div>{detail.data.deployment.status === "succeeded" && detail.data.deployment.public_url && <div className="deployment-log-public"><small>{t("deployment.publicAccess")}</small><DeploymentPublicLink url={detail.data.deployment.public_url} /></div>}</div><div className="deployment-event-list">{(detail.data.events ?? []).length ? (detail.data.events ?? []).map(event => <article className={`deployment-event ${event.status}`} key={event.id}><span className="deployment-event-marker" /><header><Status value={event.status} /><strong>{event.message || t("deployment.processStep")}</strong><time>{formatTime(event.occurred_at)}</time></header>{event.content && <pre>{event.content}</pre>}</article>) : <Empty icon={<SquareTerminal size={22} />} text={t("deployment.noLogs")} />}</div></>}</div></Dialog></div>;
}

function DeploymentPublicLink({ url }: { url: string }) {
  const value = url.trim();
  if (!value) return <span className="muted">-</span>;
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return <span className="muted">-</span>;
    const path = parsed.pathname === "/" ? "" : parsed.pathname;
    return <a className="deployment-public-link" href={value} target="_blank" rel="noreferrer" title={value}><ExternalLink size={13} /><span>{parsed.host}{path}</span></a>;
  } catch {
    return <span className="muted">-</span>;
  }
}

function deploymentDuration(deployment: Deployment) {
  const start = deployment.started_at || deployment.created_at;
  const finish = deployment.finished_at || (deployment.status === "running" || deployment.status === "preparing" ? new Date().toISOString() : null);
  if (!finish) return "-";
  const seconds = Math.max(0, Math.round((new Date(finish).getTime() - new Date(start).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return seconds < 3600 ? `${minutes}m ${seconds % 60}s` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export default DeploymentsPage;
