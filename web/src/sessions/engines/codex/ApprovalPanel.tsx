import { useEffect, useState } from "react";
import { ShieldCheck } from "lucide-react";
import { Dialog } from "../../../components/FormUI";
import { Empty, Status } from "../../../components/PageUI";
import { UserInputPrompt, isUserInputRequest } from "../../../components/UserInputPrompt";
import { formatDateTime, relative } from "../../../format";
import { useI18n } from "../../../i18n";
import type { CodexPageProps } from "../../../types";
import { ApprovalActions } from "./approvals";

function pretty(value: unknown) {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function approvalDetail(detail: unknown) {
  if (!detail || typeof detail !== "object" || Array.isArray(detail)) return pretty(detail);
  const value = detail as Record<string, unknown>;
  for (const key of ["command", "reason", "message", "question"]) {
    if (typeof value[key] === "string" && value[key]) return value[key] as string;
  }
  return pretty(detail);
}

export function readableKind(kind: string) {
  return kind.replace(/^codex\./, "").replaceAll(".", " / ").replaceAll("/", " / ");
}

export default function ApprovalPanel(props: Pick<CodexPageProps, "approvals" | "approvalSignal" | "reloadApprovals" | "notify">) {
  const { t } = useI18n();
  const [approvalOpen, setApprovalOpen] = useState(false);
  const key = props.approvals.map(item => item.id).join(",");
  useEffect(() => { setApprovalOpen(Boolean(key)); }, [key, props.approvalSignal]);
  return <>
    <button className={`approval-drawer-button ${props.approvals.length ? "visible" : ""}`} onClick={() => setApprovalOpen(true)}><ShieldCheck size={17} />{t("codex.approvalCount", { count: props.approvals.length })}</button>
    <Dialog open={approvalOpen} title={t("codex.pendingApprovals")} onClose={() => setApprovalOpen(false)} wide><div className="approval-list">{props.approvals.length === 0 ? <Empty icon={<ShieldCheck size={24} />} text={t("codex.noApprovals")} /> : props.approvals.map(item => isUserInputRequest(item) ? <UserInputPrompt key={item.id} item={item} onDecided={props.reloadApprovals} /> : <div className="approval-item" key={item.id}><div className="approval-meta"><Status value="pending" /><span>{item.title}</span><time title={relative(item.expires_at)} dateTime={item.expires_at}>{formatDateTime(item.expires_at)}</time></div><strong>{readableKind(item.kind)}</strong><pre>{approvalDetail(item.detail)}</pre><ApprovalActions item={item} onDecided={props.reloadApprovals} notify={props.notify} /></div>)}</div></Dialog>
  </>;
}
