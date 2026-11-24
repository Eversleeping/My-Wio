import { Ban,Check,LoaderCircle,ShieldCheck } from "lucide-react";
import { useState } from "react";
import { post } from "../../../api";
import { isUserInputRequest,UserInputPrompt } from "../../../components/UserInputPrompt";
import { formatDateTime, relative } from "../../../format";
import { useI18n } from "../../../i18n";
import { message,pretty } from "../../../text";
import { type Approval } from "../../../types";
import { asRecord,readableKind } from "./conversation";

export function ApprovalPrompt({ item, onDecided, notify }: { item: Approval; onDecided: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  if (isUserInputRequest(item)) return <UserInputPrompt item={item} onDecided={onDecided} />;
  return <article className="approval-prompt"><header><ShieldCheck size={16} /><strong>{t("codex.pendingApprovals")}</strong><time title={relative(item.expires_at)} dateTime={item.expires_at}>{formatDateTime(item.expires_at)}</time></header><small>{readableKind(item.kind)}</small><pre>{approvalDetail(item.detail)}</pre><ApprovalActions item={item} onDecided={onDecided} notify={notify} /></article>;
}

export function ApprovalActions({ item, onDecided, notify }: { item: Approval; onDecided: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const decide = async (decision: "approved" | "denied") => { setBusy(true); try { await post(`/approvals/${item.id}/decision`, { decision }); notify(t(decision === "approved" ? "codex.approvalGranted" : "codex.approvalDenied")); onDecided(); } catch (error) { notify(message(error)); } finally { setBusy(false); } };
  return <div className="approval-actions"><button type="button" className="secondary-button danger" disabled={busy} onClick={() => void decide("denied")}><Ban size={16} />{t("codex.deny")}</button><button type="button" className="primary-button" disabled={busy} onClick={() => void decide("approved")}>{busy ? <LoaderCircle className="spin" size={16} /> : <Check size={16} />}{t("codex.approveOnce")}</button></div>;
}

function approvalDetail(detail: unknown) { const value = asRecord(detail); if (!value) return pretty(detail); for (const key of ["command", "reason", "message", "question"]) if (typeof value[key] === "string" && value[key]) return value[key] as string; return pretty(detail); }
