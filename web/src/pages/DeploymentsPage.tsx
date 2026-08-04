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
import type { Deployment, DeploymentDetail, DeploymentSnapshot, DeploymentTarget, DeploymentTargetReview, SecretSet, Server, Workspace } from "../types";
import { useData } from "../useData";

export interface PageProps {
  realtime: number;
  notify: (text: string) => void;
}

type DeploymentContainerAction = "start" | "stop" | "restart" | "remove";
type PublicAccessMode = "port" | "domain";

type DeploymentForm = {
  source_type: "workspace" | "remote";
  workspace_id: string;
  server_id: string;
  secret_set_id: string;
  environment: string;
  repository: string;
  git_ref: string;
  compose_file: string;
  build_mode: string;
  public_access_mode: PublicAccessMode;
  public_port: string;
  public_domain: string;
  public_host: string;
  health: string;
};

function urlHost(value: string) {
  const trimmed = value.trim();
  if (!trimmed) return "";
  const candidate = trimmed.includes("://") ? trimmed : `http://${trimmed}`;
  try {
    return new URL(candidate).hostname;
  } catch {
    return "";
  }
}

function parsePublicAccess(value: string): { mode: PublicAccessMode; port: string; domain: string; host: string } {
  const trimmed = value.trim();
  if (!trimmed) return { mode: "port", port: "", domain: "", host: "" };
  const candidate = trimmed.includes("://") ? trimmed : `http://${trimmed}`;
  try {
    const parsed = new URL(candidate);
    if (parsed.port) return { mode: "port", port: parsed.port, domain: "", host: parsed.hostname };
  } catch {
    return { mode: "domain", port: "", domain: trimmed, host: "" };
  }
  return { mode: "domain", port: "", domain: trimmed, host: "" };
}

function publicURLForPort(server: Server | undefined, port: string, fallbackHost: string) {
  const value = Number(port);
  if (!Number.isInteger(value) || value < 1 || value > 65535) return "";
  const host = urlHost(server?.address || "") || urlHost(server?.hostname || "") || fallbackHost.trim();
  if (!host) return "";
  const formattedHost = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return `http://${formattedHost}:${value}`;
}

function healthChecksFor(value: string) {
  return value.split(/\r?\n/).map(address => address.trim()).filter(Boolean).map(address => ({ type: address.startsWith("http") ? "http" : "tcp", address, timeout_seconds: 60 }));
}

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

function reviewValue(config: DeploymentTargetReview["current"], field: string) {
  switch (field) {
    case "server": return config.server_name || config.server_id;
    case "workspace": return config.workspace_path || config.workspace_name || config.workspace_id;
    case "secret_set": return config.secret_set_name || "-";
    case "secret_set_key_version": return config.secret_set_key_version ? `v${config.secret_set_key_version}` : "-";
    default: return String(config[field as keyof typeof config] ?? "");
  }
}

const deploymentReviewFields = ["source_type", "environment", "server", "workspace", "repository", "git_ref", "compose_file", "working_dir", "build_mode", "release_root", "configured_public_url", "detected_public_url", "health_checks", "secret_set", "secret_set_key_version"];

function reviewWithCurrentConfig(review: DeploymentTargetReview | null, current: DeploymentTargetReview["current"] | null) {
  if (!review || !current) return review;
  const previous = review.last_successful;
  const changes = previous ? deploymentReviewFields.map(field => ({ field, previous: reviewValue(previous, field), current: reviewValue(current, field) })).filter(change => change.previous !== change.current) : [];
  return { ...review, current, changes };
}

function DeploymentConfigReview({ review, loading, error }: { review: DeploymentTargetReview | null; loading: boolean; error: string }) {
  const { t } = useI18n();
  const labels: Record<string, string> = {
    source_type: t("deployment.sourceType"),
    environment: t("column.environment"),
    server: t("column.server"),
    workspace: t("deployment.workspace"),
    repository: t("deployment.repository"),
    git_ref: t("column.gitRef"),
    compose_file: t("deployment.composeFile"),
    working_dir: t("deployment.workingDirectory"),
    build_mode: t("deployment.buildMode"),
    release_root: t("deployment.releaseRoot"),
    configured_public_url: t("deployment.publicURL"),
    detected_public_url: t("deployment.detectedURL"),
    health_checks: t("deployment.healthCheck"),
    secret_set: t("deployment.secretSet"),
    secret_set_key_version: t("deployment.secretVersion")
  };
  return <section className="deployment-config-review" aria-live="polite"><header><div><strong>{t("deployment.configReview")}</strong><small>{t("deployment.configReviewHint")}</small></div>{loading && <LoaderCircle className="spin" size={15} />}</header>{error && <ErrorBanner text={error} />}{!loading && !error && review && !review.snapshot_available && <div className="snapshot-notice warning"><AlertTriangle size={15} />{t("deployment.noSuccessfulBaseline")}</div>}{review?.snapshot_available && review.last_successful && <><div className="deployment-review-meta"><span>{t("deployment.lastSuccessful")}</span><code>{shortSHA(review.last_successful.resolved_commit || review.last_successful.git_ref)}</code><time>{formatTime(review.last_successful.created_at)}</time></div>{review.changes.length === 0 ? <p className="deployment-review-empty">{t("deployment.noConfigChanges")}</p> : <div className="deployment-review-list">{review.changes.map(change => <div className="deployment-review-row" key={change.field}><strong>{labels[change.field] || change.field}</strong><code title={change.previous}>{change.previous || "-"}</code><span aria-hidden="true">-&gt;</span><code title={change.current}>{change.current || "-"}</code></div>)}</div>}<details className="deployment-review-details"><summary>{t("deployment.showCurrentConfig")}</summary><div className="deployment-review-list">{Object.keys(labels).map(field => <div className="deployment-review-row" key={field}><strong>{labels[field]}</strong><code title={reviewValue(review.last_successful!, field)}>{reviewValue(review.last_successful!, field) || "-"}</code><span aria-hidden="true">-&gt;</span><code title={reviewValue(review.current, field)}>{reviewValue(review.current, field) || "-"}</code></div>)}</div></details></>}</section>;
}

function DeploymentSnapshotSummary({ snapshot, error }: { snapshot?: DeploymentSnapshot; error?: string }) {
  const { t } = useI18n();
  if (error) return <div className="snapshot-notice warning"><AlertTriangle size={15} />{t("deployment.snapshotUnavailable")}</div>;
  if (!snapshot) return null;
  return <section className="deployment-snapshot-summary"><header><strong>{t("deployment.configurationSnapshot")}</strong><small>{formatTime(snapshot.created_at)}</small></header><div className="deployment-snapshot-grid"><span>{t("column.commit")}</span><code>{shortSHA(snapshot.resolved_commit || snapshot.git_ref)}</code><span>{t("deployment.composeFile")}</span><code>{snapshot.compose_file || "-"}</code><span>{t("deployment.releaseRoot")}</span><code>{snapshot.release_root || "-"}</code><span>{t("deployment.secretSet")}</span><span>{snapshot.secret_set_name ? `${snapshot.secret_set_name} (${t("deployment.secretVersion")} ${snapshot.secret_set_key_version})` : t("common.none")}</span></div></section>;
}

function HistoryRollbackButton({ item, targets, servers, busy, onSelect }: { item: Deployment; targets: DeploymentTarget[]; servers: Server[]; busy: string; onSelect: (target: DeploymentTarget, deploymentID: string, commit: string) => void }) {
  const { t } = useI18n();
  const target = targets.find(candidate => candidate.id === item.target_id);
  if (!target || item.status !== "succeeded" || !item.snapshot_available) return null;
  const server = servers.find(candidate => candidate.id === target.server_id);
  const online = server?.status === "online";
  const supported = server?.exact_rollback_supported === true;
  const available = online && supported && target.container_status !== "pending";
  const title = available ? t("deployment.rollbackVersion") : online && !supported ? t("deployment.rollbackRequiresAgentUpgrade") : t("deployment.rollbackUnavailable");
  return <button className="icon-button" disabled={!available || busy !== ""} title={title} aria-label={t("deployment.rollbackVersion")} onClick={() => onSelect(target, item.id, item.resolved_commit || item.commit_ref)}><Undo2 size={15} /></button>;
}

export function DeploymentsPage({ realtime, notify }: PageProps) {
  const { t } = useI18n();
  const targets = useData<DeploymentTarget[]>("/deployment-targets", realtime);
  const deploymentRequest = useData<Deployment[]>("/deployments", realtime);
  const deployments = {
    ...deploymentRequest,
    data: deploymentRequest.data?.map(item => item.snapshot_available ? item : { ...item, project_name: t("deployment.snapshotUnavailable"), environment: "", public_url: "" }) ?? deploymentRequest.data
  };
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const servers = useData<Server[]>("/servers", realtime);
  const secrets = useData<SecretSet[]>("/secret-sets", realtime);
  const emptyForm: DeploymentForm = { source_type: "workspace", workspace_id: "", server_id: "", secret_set_id: "", environment: "production", repository: "", git_ref: "", compose_file: "compose.yaml", build_mode: "build", public_access_mode: "port", public_port: "", public_domain: "", public_host: "", health: "" };
  const [targetDialog, setTargetDialog] = useState(false);
  const [editingTarget, setEditingTarget] = useState<DeploymentTarget | null>(null);
  const [form, setForm] = useState(emptyForm);
  const [busy, setBusy] = useState("");
  const [detailID, setDetailID] = useState("");
  const [deploymentConfirmation, setDeploymentConfirmation] = useState<
    | { type: "target"; target: DeploymentTarget }
    | { type: "history"; deployment: Deployment }
    | { type: "rollback"; target: DeploymentTarget; sourceDeploymentID?: string; sourceCommit?: string }
    | { type: "container"; target: DeploymentTarget; action: Exclude<DeploymentContainerAction, "start"> }
    | null
  >(null);
  const detail = useData<DeploymentDetail>(detailID ? `/deployments/${detailID}` : null, realtime);
  const targetReviewRequest = useData<DeploymentTargetReview>(editingTarget ? `/deployment-targets/${editingTarget.id}/review` : null, realtime);
  const active = (status: string) => ["queued", "preparing", "running"].includes(status);
  const openCreate = () => { setEditingTarget(null); setForm(emptyForm); setTargetDialog(true); };
  const openEdit = (target: DeploymentTarget) => {
    let health = "";
    try { health = (JSON.parse(target.health_checks) as Array<{ address?: string }>).map(check => check.address ?? "").filter(Boolean).join("\n"); } catch { health = ""; }
    const publicAccess = parsePublicAccess(target.configured_public_url || "");
    setEditingTarget(target);
    setForm({ source_type: target.source_type || "remote", workspace_id: target.workspace_id || "", server_id: target.server_id, secret_set_id: target.secret_set_id, environment: target.environment, repository: target.repository, git_ref: target.git_ref, compose_file: target.compose_file, build_mode: target.build_mode, public_access_mode: publicAccess.mode, public_port: publicAccess.port, public_domain: publicAccess.domain, public_host: publicAccess.host, health });
    setTargetDialog(true);
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const selectedServer = (servers.data ?? []).find(server => server.id === form.server_id);
    const { health, public_access_mode, public_port, public_domain, public_host, ...target } = form;
    const public_url = public_access_mode === "port" ? publicURLForPort(selectedServer, public_port, public_host) : public_domain.trim();
    const health_checks = healthChecksFor(health);
    setBusy(editingTarget ? `edit:${editingTarget.id}` : "create");
    try {
      if (editingTarget) await put(`/deployment-targets/${editingTarget.id}`, { ...target, public_url, health_checks });
      else await post("/deployment-targets", { ...target, public_url, health_checks });
      setTargetDialog(false);
      targets.reload();
      notify(t(editingTarget ? "deployment.targetUpdated" : "deployment.targetCreated"));
    } catch (err) { notify(message(err)); } finally { setBusy(""); }
  };
  const run = async (target: DeploymentTarget) => { setBusy(`deploy:${target.id}`); try { const response = await post<{ deployment: Deployment }>(`/deployment-targets/${target.id}/deploy`, { commit_ref: target.git_ref }); targets.reload(); deployments.reload(); setDetailID(response.deployment.id); notify(t("deployment.queued")); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const rollback = async (target: DeploymentTarget, sourceDeploymentID = "") => { setBusy(`rollback:${target.id}`); try { const response = await post<{ deployment: Deployment }>(`/deployment-targets/${target.id}/rollback`, sourceDeploymentID ? { deployment_id: sourceDeploymentID } : {}); targets.reload(); deployments.reload(); setDetailID(response.deployment.id); notify(t("deployment.rollbackQueued")); setDeploymentConfirmation(null); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
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
  const selectedServer = (servers.data ?? []).find(server => server.id === form.server_id);
  const publicAccessPreview = form.public_access_mode === "port" ? publicURLForPort(selectedServer, form.public_port, form.public_host) : form.public_domain.trim();
  const selectedWorkspace = (workspaces.data ?? []).find(workspace => workspace.id === form.workspace_id);
  const selectedSecret = (secrets.data ?? []).find(secret => secret.id === form.secret_set_id);
  const reviewCurrent = targetReviewRequest.data && editingTarget ? {
    ...targetReviewRequest.data.current,
    source_type: form.source_type,
    environment: form.environment,
    server_id: form.server_id,
    server_name: selectedServer?.name ?? "",
    workspace_id: form.source_type === "workspace" ? form.workspace_id : "",
    workspace_path: form.source_type === "workspace" ? selectedWorkspace?.path ?? "" : "",
    workspace_name: form.source_type === "workspace" ? selectedWorkspace?.display_name ?? "" : "",
    repository: form.source_type === "remote" ? form.repository : "",
    git_ref: form.git_ref,
    compose_file: form.compose_file,
    build_mode: form.build_mode,
    configured_public_url: publicAccessPreview,
    health_checks: JSON.stringify(healthChecksFor(form.health)),
    secret_set_id: form.secret_set_id,
    secret_set_name: selectedSecret?.name ?? "",
    secret_set_key_version: selectedSecret?.key_version ?? 0,
    secret_set_updated_at: selectedSecret?.updated_at ?? null
  } : null;
  const formReview = reviewWithCurrentConfig(targetReviewRequest.data, reviewCurrent);
  const targetReview = { ...targetReviewRequest, data: formReview };
  return <div className="page-stack deployment-page"><Section title={t("deployment.targets")} icon={<Rocket size={18} />} action={<button className="primary-button" onClick={openCreate}><Plus size={17} />{t("deployment.newTarget")}</button>}><DataTable headers={[t("column.project"), t("column.environment"), t("column.server"), t("deployment.containerState"), t("column.gitRef"), t("column.compose"), t("deployment.publicAccess"), t("common.actions")]} empty={t("deployment.noTargets")}>{(targets.data ?? []).map(target => {
    const sourcePath = target.source_type === "workspace" ? target.workspace_path : target.repository;
    const containerStatus = target.container_status || "unknown";
    const targetServer = (servers.data ?? []).find(server => server.id === target.server_id);
    const serverOnline = targetServer?.status === "online";
    const rollbackSupported = targetServer?.exact_rollback_supported === true;
    const hasRollbackBaseline = deployments.data === undefined || (deployments.data ?? []).some(item => item.target_id === target.id && item.snapshot_available && (item.status === "succeeded" || item.status === "rolled_back"));
    const locked = busy !== "" || deploymentConfirmation !== null || containerStatus === "pending" || !serverOnline;
    const disabledReason = busy !== "" || deploymentConfirmation !== null ? t("deployment.actionBusy") : containerStatus === "pending" ? t("deployment.containerPending") : !serverOnline ? t("deployment.serverOffline") : "";
    const primaryContainerAction: DeploymentContainerAction = containerStatus === "running" ? "stop" : "start";
    const primaryContainerLabel = t(primaryContainerAction === "stop" ? "deployment.containerStop" : "deployment.containerStart");
    const actions: ContextMenuAction[] = [
      { id: "rollback", label: t("deployment.rollback"), icon: Undo2, disabled: locked || !rollbackSupported || !hasRollbackBaseline, disabledReason: disabledReason || (!rollbackSupported ? t("deployment.rollbackRequiresAgentUpgrade") : !hasRollbackBaseline ? t("deployment.noRollbackBaseline") : undefined), onSelect: () => setDeploymentConfirmation({ type: "rollback", target }) },
      { id: "edit", label: t("deployment.editTarget"), icon: Pencil, disabled: busy !== "", onSelect: () => openEdit(target) },
      { id: "delete-target", label: t("deployment.deleteTarget"), icon: Trash2, danger: true, separatorBefore: true, disabled: busy !== "" || containerStatus === "pending", onSelect: () => setDeploymentConfirmation({ type: "target", target }) }
    ];
    return <tr key={target.id}><td><div className="cell-main"><strong>{target.project_name}</strong><small title={sourcePath}>{sourcePath}</small></div></td><td><Status value={target.environment} /></td><td>{target.server_name}</td><td><div className="cell-main container-state-cell"><Status value={containerStatus} />{target.container_message && <small title={target.container_message}>{target.container_message}</small>}</div></td><td><code>{target.git_ref}</code></td><td><code>{target.compose_file}</code></td><td><DeploymentPublicLink url={target.public_url} /></td><td><div className="row-actions deployment-target-actions"><button type="button" className="primary-button small" disabled={locked} title={disabledReason || undefined} onClick={() => void run(target)}>{busy === `deploy:${target.id}` ? <LoaderCircle className="spin" size={14} /> : <Rocket size={14} />}{t("deployment.deploy")}</button><button type="button" className="secondary-button small deployment-lifecycle-button" disabled={locked} title={disabledReason || primaryContainerLabel} onClick={() => requestContainerAction(target, primaryContainerAction)}>{busy === `container:${primaryContainerAction}:${target.id}` ? <LoaderCircle className="spin" size={14} /> : primaryContainerAction === "stop" ? <Square size={14} /> : <Play size={14} />}{primaryContainerLabel}</button><button type="button" className="icon-button" disabled={locked || containerStatus === "stopped" || containerStatus === "removed"} title={disabledReason || t("deployment.containerRestart")} aria-label={t("deployment.containerRestart")} onClick={() => requestContainerAction(target, "restart")}>{busy === `container:restart:${target.id}` ? <LoaderCircle className="spin" size={14} /> : <RefreshCw size={14} />}</button><button type="button" className="icon-button danger" disabled={locked || containerStatus === "removed"} title={disabledReason || t("deployment.containerRemove")} aria-label={t("deployment.containerRemove")} onClick={() => requestContainerAction(target, "remove")}>{busy === `container:remove:${target.id}` ? <LoaderCircle className="spin" size={14} /> : <Trash2 size={14} />}</button><ContextMenu label={t("deployment.actions")} actions={actions}>{null}</ContextMenu></div></td></tr>;
  })}</DataTable></Section><Section title={t("deployment.history")} icon={<History size={18} />} action={<button className="icon-button" title={t("common.refresh")} onClick={deployments.reload}><RefreshCw size={16} /></button>}><DataTable headers={[t("column.project"), t("column.environment"), t("column.commit"), t("column.status"), t("deployment.duration"), t("column.message"), t("column.created"), t("deployment.publicAccess"), t("common.actions")]} empty={t("deployment.noHistory")}>{(deployments.data ?? []).map(item => <tr key={item.id}><td><strong>{item.project_name}</strong></td><td>{item.environment}</td><td><code>{shortSHA(item.resolved_commit || item.commit_ref)}</code></td><td><Status value={item.status} /></td><td className="deployment-duration">{deploymentDuration(item)}</td><td className="message-cell" title={item.message}>{item.message || "-"}</td><td>{relative(item.created_at)}</td><td>{item.status === "succeeded" ? <DeploymentPublicLink url={item.public_url} /> : <span className="muted">-</span>}</td><td><div className="row-actions"><button className="icon-button" title={t("deployment.viewLogs")} onClick={() => setDetailID(item.id)}><SquareTerminal size={15} /></button><HistoryRollbackButton item={item} targets={targets.data ?? []} servers={servers.data ?? []} busy={busy} onSelect={(target, deploymentID, commit) => setDeploymentConfirmation({ type: "rollback", target, sourceDeploymentID: deploymentID, sourceCommit: commit })} /><button className="icon-button danger" disabled={active(item.status) || busy !== ""} title={active(item.status) ? t("deployment.activeCannotDelete") : t("deployment.deleteHistory")} onClick={() => setDeploymentConfirmation({ type: "history", deployment: item })}><Trash2 size={15} /></button></div></td></tr>)}</DataTable></Section>
  {deploymentConfirmation?.type === "target" ? <ConfirmDialog open danger title={t("deployment.deleteTarget")} impact={t("deployment.confirmDeleteTarget", { project: deploymentConfirmation.target.project_name, environment: deploymentConfirmation.target.environment })} confirmLabel={t("deployment.deleteTarget")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy !== ""} onClose={() => setDeploymentConfirmation(null)} onConfirm={() => deleteTarget(deploymentConfirmation.target)} /> : deploymentConfirmation?.type === "history" ? <ConfirmDialog open danger title={t("deployment.deleteHistory")} impact={t("deployment.confirmDeleteHistory")} confirmLabel={t("deployment.deleteHistory")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy !== ""} onClose={() => setDeploymentConfirmation(null)} onConfirm={() => deleteHistory(deploymentConfirmation.deployment)} /> : deploymentConfirmation?.type === "rollback" ? <ConfirmDialog open danger title={t("deployment.rollback")} impact={deploymentConfirmation.sourceDeploymentID ? t("deployment.confirmRollbackVersion", { project: deploymentConfirmation.target.project_name, environment: deploymentConfirmation.target.environment, commit: shortSHA(deploymentConfirmation.sourceCommit || deploymentConfirmation.sourceDeploymentID) }) : t("deployment.confirmRollback", { project: deploymentConfirmation.target.project_name, environment: deploymentConfirmation.target.environment })} confirmLabel={t("deployment.rollback")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy !== ""} onClose={() => setDeploymentConfirmation(null)} onConfirm={() => rollback(deploymentConfirmation.target, deploymentConfirmation.sourceDeploymentID)} /> : deploymentConfirmation?.type === "container" ? <ConfirmDialog open danger title={t(deploymentConfirmation.action === "stop" ? "deployment.containerStop" : deploymentConfirmation.action === "restart" ? "deployment.containerRestart" : "deployment.containerRemove")} impact={t(deploymentConfirmation.action === "stop" ? "deployment.confirmContainerStop" : deploymentConfirmation.action === "restart" ? "deployment.confirmContainerRestart" : "deployment.confirmContainerRemove", { project: deploymentConfirmation.target.project_name, environment: deploymentConfirmation.target.environment })} confirmLabel={t(deploymentConfirmation.action === "stop" ? "deployment.containerStop" : deploymentConfirmation.action === "restart" ? "deployment.containerRestart" : "deployment.containerRemove")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy !== ""} onClose={() => setDeploymentConfirmation(null)} onConfirm={() => manageContainer(deploymentConfirmation.target, deploymentConfirmation.action)} /> : null}
  <Dialog open={targetDialog} title={t(editingTarget ? "deployment.editTargetTitle" : "deployment.targetTitle")} onClose={() => setTargetDialog(false)} wide>{editingTarget && <DeploymentConfigReview review={targetReview.data} loading={targetReview.loading} error={targetReview.error} />}<form onSubmit={submit}><div className="segmented-control deployment-source-control" role="group" aria-label={t("deployment.sourceType")}><button type="button" className={form.source_type === "workspace" ? "active" : ""} onClick={() => setForm({ ...form, source_type: "workspace", repository: "", git_ref: "", workspace_id: "" })}><ServerIcon size={15} />{t("deployment.sourceWorkspace")}</button><button type="button" className={form.source_type === "remote" ? "active" : ""} onClick={() => setForm({ ...form, source_type: "remote", workspace_id: "", git_ref: form.git_ref || "main" })}><GitBranch size={15} />{t("deployment.sourceRemote")}</button></div><div className="form-grid"><Field label={t("column.server")}><select value={form.server_id} onChange={e => setForm({ ...form, server_id: e.target.value, workspace_id: "", public_host: "" })} required><option value="">{t("deployment.selectServer")}</option>{(servers.data ?? []).filter(item => item.status === "online").map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>{form.source_type === "workspace" ? <Field label={t("deployment.workspace")}><select value={form.workspace_id} onChange={e => { const workspace = availableWorkspaces.find(item => item.id === e.target.value); setForm({ ...form, workspace_id: e.target.value, git_ref: workspace?.branch || "" }); }} required><option value="">{t("deployment.selectWorkspace")}</option>{availableWorkspaces.map(item => <option key={item.id} value={item.id}>{item.project_name} · {item.display_name || item.path}</option>)}</select></Field> : <Field label={t("deployment.repository")}><input value={form.repository} onChange={e => setForm({ ...form, repository: e.target.value })} placeholder="https://example.com/team/project.git" required /></Field>}<Field label={t("column.environment")}><input value={form.environment} onChange={e => setForm({ ...form, environment: e.target.value })} required /></Field><Field label={t("deployment.secretSet")}><select value={form.secret_set_id} onChange={e => setForm({ ...form, secret_set_id: e.target.value })}><option value="">{t("common.none")}</option>{(secrets.data ?? []).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field></div><div className="form-grid thirds"><Field label={t("column.gitRef")}><input value={form.git_ref} onChange={e => setForm({ ...form, git_ref: e.target.value })} placeholder={form.source_type === "workspace" ? t("deployment.currentBranch") : "main"} /></Field><Field label={t("deployment.composeFile")}><input value={form.compose_file} onChange={e => setForm({ ...form, compose_file: e.target.value })} /></Field><Field label={t("deployment.buildMode")}><select value={form.build_mode} onChange={e => setForm({ ...form, build_mode: e.target.value })}><option value="build">{t("deployment.build")}</option><option value="pull">{t("deployment.pull")}</option></select></Field></div><div className="form-grid deployment-public-access-grid"><Field label={t("deployment.publicAccessMode")}><select value={form.public_access_mode} onChange={e => setForm({ ...form, public_access_mode: e.target.value as PublicAccessMode })} aria-label={t("deployment.publicAccessMode")} required><option value="port">{t("deployment.publicAccessPort")}</option><option value="domain">{t("deployment.publicAccessDomain")}</option></select></Field>{form.public_access_mode === "port" ? <Field label={t("deployment.publicPort")}><input type="number" inputMode="numeric" min="1" max="65535" step="1" value={form.public_port} onChange={e => setForm({ ...form, public_port: e.target.value })} placeholder={t("deployment.publicPortPlaceholder")} aria-label={t("deployment.publicPort")} />{publicAccessPreview && <small className="deployment-public-access-preview">{publicAccessPreview}</small>}</Field> : <Field label={t("deployment.publicDomain")}><input type="text" inputMode="url" autoComplete="url" value={form.public_domain} onChange={e => setForm({ ...form, public_domain: e.target.value })} placeholder={t("deployment.publicDomainPlaceholder")} aria-label={t("deployment.publicDomain")} />{publicAccessPreview && <small className="deployment-public-access-preview">{publicAccessPreview}</small>}</Field>}</div><Field label={t("deployment.healthCheck")}><textarea rows={2} value={form.health} onChange={e => setForm({ ...form, health: e.target.value })} placeholder={t("deployment.healthPlaceholder")} /></Field><div className="deployment-preflight-note"><ShieldCheck size={17} /><span>{t("deployment.preflightNote")}</span></div><DialogActions><button type="button" className="secondary-button" onClick={() => setTargetDialog(false)}>{t("common.cancel")}</button><button className="primary-button" disabled={busy !== ""}>{busy ? <LoaderCircle className="spin" size={16} /> : editingTarget ? <Check size={16} /> : <Rocket size={16} />}{t(editingTarget ? "deployment.saveTarget" : "deployment.createTarget")}</button></DialogActions></form></Dialog>
  <Dialog open={Boolean(detailID)} title={t("deployment.logTitle")} onClose={() => setDetailID("")} wide className="deployment-log-dialog"><div className="deployment-log-content">{detail.loading && <div className="deployment-log-loading"><LoaderCircle className="spin" size={20} />{t("common.loading")}</div>}{detail.error && <ErrorBanner text={detail.error} />}{detail.data && <><div className="deployment-log-summary"><div><small>{t("column.project")}</small><strong>{detail.data.deployment.project_name}</strong></div><div><small>{t("column.environment")}</small><Status value={detail.data.deployment.environment} /></div><div><small>{t("column.commit")}</small><code>{shortSHA(detail.data.deployment.resolved_commit || detail.data.deployment.commit_ref)}</code></div><div><small>{t("deployment.duration")}</small><strong>{deploymentDuration(detail.data.deployment)}</strong></div>{detail.data.deployment.status === "succeeded" && detail.data.deployment.public_url && <div className="deployment-log-public"><small>{t("deployment.publicAccess")}</small><DeploymentPublicLink url={detail.data.deployment.public_url} /></div>}</div><DeploymentSnapshotSummary snapshot={detail.data.deployment.snapshot} error={detail.data.deployment.snapshot_error} /><div className="deployment-event-list">{(detail.data.events ?? []).length ? (detail.data.events ?? []).map(event => <article className={`deployment-event ${event.status}`} key={event.id}><span className="deployment-event-marker" /><header><Status value={event.status} /><strong>{event.message || t("deployment.processStep")}</strong><time>{formatTime(event.occurred_at)}</time></header>{event.content && <pre>{event.content}</pre>}</article>) : <Empty icon={<SquareTerminal size={22} />} text={t("deployment.noLogs")} />}</div></>}</div></Dialog></div>;
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
