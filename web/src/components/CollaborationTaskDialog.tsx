import { EngineUnavailable, useWorkspaceEngines } from "../sessions/availability";
import { type FormEvent, type RefObject, useEffect, useState } from "react";
import { Network } from "lucide-react";
import { useI18n } from "../i18n";
import { useData } from "../useData";
import { useCodexModels, reasoningOptionsFor } from "../codexModels";
import { useClaudeModels } from "../claudeModels";
import { useGeminiModels } from "../geminiModels";
import { geminiThinkingLevels } from "./GeminiThinkingPicker";
import { Dialog, DialogActions } from "./Dialog";

export type CollaborationEngine = "codex" | "claude" | "gemini";
type Grant = { enabled: boolean; worktree: boolean; target_modes: Partial<Record<CollaborationEngine, string>> | null };
type State = { grant: Grant; supported: boolean };
type Option = { value: string; label: string };

export type CollaborationTask = { engine: CollaborationEngine; model: string; effort: string; directoryMode: "shared" | "worktree"; title: string; content: string };

const engineNames: Record<CollaborationEngine, string> = { codex: "Codex", claude: "Claude", gemini: "Gemini" };
const claudeAliases = ["sonnet", "opus", "haiku"];

// The selections are written as exact tool arguments so the source model does
// not have to guess model IDs or effort names.
export function collaborationTaskPrompt(task: CollaborationTask, zh: boolean) {
  const args: Record<string, string> = { engine: task.engine };
  if (task.model) args.model = task.model;
  if (task.effort) args.effort = task.effort;
  args.directory_mode = task.directoryMode;
  if (task.title.trim()) args.title = task.title.trim();
  const json = JSON.stringify(args, null, 2);
  const omitted = [!task.model && "model", !task.effort && "effort"].filter(Boolean).join(zh ? "、" : ", ");
  return zh ? [
    "请调用 wio_create_session 创建一个新的协作会话来处理下面的协作内容。",
    "以下参数由用户在界面中选定，必须原样传入，不要修改、替换或补充其他模型和思考等级：",
    "```json", json, "```",
    ...(omitted ? [`未列出的 ${omitted} 不要传，使用默认值。`] : []),
    "prompt 参数：以协作内容为准，完整保留用户的全部要求；可以补充子会话完成任务所需的当前会话上下文（相关文件、已知结论等）。request_id 自行生成。",
    "创建后告诉我新会话的标题和状态。",
    "", "协作内容：", task.content.trim(),
  ].join("\n") : [
    "Call wio_create_session to create a new collaboration session for the task below.",
    "The user chose these arguments in the UI. Pass them exactly as given; do not change them or add another model or effort:",
    "```json", json, "```",
    ...(omitted ? [`Do not pass ${omitted}; use the default.`] : []),
    "prompt: based on the task below. Keep every requirement, and add any context from this session the child needs (relevant files, known findings). Generate your own request_id.",
    "After creating it, tell me the new session's title and status.",
    "", "Task:", task.content.trim(),
  ].join("\n");
}

export function CollaborationTaskDialog({ open, engine, sessionID, workspaceID, currentModel = "", onClose, onSubmit }: { open: boolean; engine: CollaborationEngine; sessionID: string; workspaceID: string; currentModel?: string; onClose: () => void; onSubmit: (prompt: string) => void }) {
  const { language, t } = useI18n();
  const zh = language === "zh-CN";
  const [target, setTarget] = useState<CollaborationEngine>(engine);
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [directoryMode, setDirectoryMode] = useState<"shared" | "worktree">("shared");
  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  useEffect(() => { if (open) { setTarget(engine); setModel(""); setEffort(""); setDirectoryMode("shared"); setTitle(""); setContent(""); } }, [open, engine, sessionID]);
  const state = useData<State>(open ? `/sessions/${engine}/${encodeURIComponent(sessionID)}/collaboration` : null, open);
  const availability = useWorkspaceEngines(open ? workspaceID : "");
  const targetStatus = availability.data?.engines?.find(item => item.engine === target);
  const targetReady = Boolean(targetStatus?.available) && !availability.error;
  const codex = useCodexModels(open && targetReady && target === "codex" ? workspaceID : undefined);
  const claude = useClaudeModels(open && targetReady && target === "claude" ? workspaceID : "", sessionID);
  const gemini = useGeminiModels(open && targetReady && target === "gemini" ? workspaceID : "");

  const sameEngine = target === engine;
  // Omitting the model inherits the caller's model only within the same engine.
  const defaultModel = sameEngine ? (currentModel ? t("collaboration.currentModel", { model: currentModel }) : t("collaboration.sameModel")) : t("collaboration.serverDefaultModel");
  const models: Option[] = target === "codex" ? codex.models.map(item => ({ value: item.model, label: item.displayName && item.displayName !== item.model ? `${item.displayName} · ${item.model}` : item.model }))
    : target === "claude" ? [...claude.models.map(item => ({ value: item.id, label: item.name && item.name !== item.id ? `${item.name} · ${item.id}` : item.id })), ...claudeAliases.filter(alias => !claude.models.some(item => item.id === alias)).map(alias => ({ value: alias, label: alias }))]
    : (gemini.data?.models ?? []).map(item => ({ value: item.id, label: item.name && item.name !== item.id ? `${item.name} · ${item.id}` : item.id }));
  const modelsLoading = target === "codex" ? codex.loading : target === "gemini" ? gemini.loading : false;
  const modelsError = target === "codex" ? codex.error : target === "claude" ? claude.error : gemini.error;
  // Effort without an explicit model would be checked against an unknown default.
  const efforts: Option[] = target === "codex" ? reasoningOptionsFor(codex.models, model).map(option => ({ value: option.value, label: option.labelKey ? `${t(option.labelKey)} · ${option.value}` : option.value }))
    : target === "claude" ? ["low", "medium", "high", "xhigh", "max"].map(value => ({ value, label: `${t(`claude.effort.${value}`)} · ${value}` }))
    : geminiThinkingLevels(model).map(value => ({ value, label: `${t(`gemini.thinking.${value}`)} · ${value}` }));
  const effortSignature = efforts.map(option => option.value).join(",");
  useEffect(() => { if (effort && !effortSignature.split(",").includes(effort)) setEffort(""); }, [effort, effortSignature]);

  const grant = state.data?.grant;
  const blocker = availability.error ? availability.error : state.error ? state.error
    : !state.data ? ""
    : !state.data.supported ? t("collaboration.updateAgent")
    : !grant?.enabled ? t("collaboration.disabled")
    : !sameEngine && !grant.target_modes?.[target] ? t("collaboration.creationNotGranted", { engine: engineNames[target] })
    : "";
  const worktreeAllowed = Boolean(grant?.worktree);
  useEffect(() => { if (!worktreeAllowed && directoryMode === "worktree") setDirectoryMode("shared"); }, [worktreeAllowed, directoryMode]);
  const ready = Boolean(state.data) && targetReady && !blocker && Boolean(content.trim());
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!ready) return;
    onSubmit(collaborationTaskPrompt({ engine: target, model, effort, directoryMode, title, content }, zh));
  };
  return <Dialog open={open} title={t("collaboration.create")} onClose={onClose} closeLabel={t("common.close")} wide>
    <form className="collaboration-task" onSubmit={submit}>
      <p>{t("collaboration.createInstructions")}</p>
      <div className="form-grid">
        <label className="field"><span>{t("collaboration.agent")}</span><select aria-label={t("collaboration.agent")} value={target} onChange={event => { setTarget(event.target.value as CollaborationEngine); setModel(""); setEffort(""); }}>
          {(["codex", "claude", "gemini"] as const).map(value => <option key={value} value={value} disabled={!availability.data?.engines?.find(item => item.engine === value)?.available}>{engineNames[value]}{value === engine ? t("collaboration.current") : ""}</option>)}
        </select></label>
        <label className="field"><span>{t("collaboration.directory")}</span><select aria-label={t("collaboration.directory")} value={directoryMode} onChange={event => setDirectoryMode(event.target.value as "shared" | "worktree")}>
          <option value="shared">{t("collaboration.shareDirectory")}</option>
          <option value="worktree" disabled={!worktreeAllowed}>{t("collaboration.separateWorktree")}{worktreeAllowed ? "" : t("collaboration.worktreeNotGranted")}</option>
        </select></label>
      </div>
      <div className="form-grid">
        <label className="field"><span>{t("collaboration.model")}</span><select aria-label={t("collaboration.model")} value={model} onChange={event => setModel(event.target.value)}>
          <option value="">{defaultModel}</option>
          {models.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select>{modelsLoading && <small className="muted" role="status">{t("collaboration.loadingModels")}</small>}{modelsError && <small className="gemini-model-error" role="status">{t("collaboration.modelsFailed")}{modelsError}</small>}</label>
        <label className="field"><span>{t("collaboration.thinkingLevel")}</span><select aria-label={t("collaboration.thinkingLevel")} value={effort} disabled={!efforts.length} onChange={event => setEffort(event.target.value)}>
          <option value="">{target === "gemini" && !model ? t("collaboration.chooseGeminiModel") : t("collaboration.default")}</option>
          {efforts.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select></label>
      </div>
      <label className="field"><span>{t("collaboration.optionalTitle")}</span><input aria-label={t("collaboration.sessionTitle")} value={title} maxLength={180} onChange={event => setTitle(event.target.value)} /></label>
      <label className="field"><span>{t("collaboration.task")}</span><textarea aria-label={t("collaboration.task")} rows={6} required maxLength={20000} value={content} placeholder={t("collaboration.taskPlaceholder")} onChange={event => setContent(event.target.value)} /></label>
      {state.loading && !state.data && <p className="muted" role="status">{t("common.loading")}</p>}
      {targetStatus && !targetStatus.available && <EngineUnavailable reason={targetStatus.reason} />}
      {availability.loading && <p className="muted" role="status">{t("common.loading")}</p>}
      {blocker && <p className="error-banner" role="alert">{blocker}</p>}
      <DialogActions><button type="button" className="secondary-button" onClick={onClose}>{t("common.cancel")}</button><button className="primary-button" disabled={!ready}><Network size={16} />{t("collaboration.sendToModel")}</button></DialogActions>
    </form>
  </Dialog>;
}

// Submits the composer form once a generated prompt has been rendered, so the
// page's normal send path (validation, steering, request IDs) is reused.
export function useSubmitPrompt(formRef: RefObject<HTMLFormElement>, prompt: string, setPrompt: (value: string) => void) {
  const [pending, setPending] = useState<string | null>(null);
  useEffect(() => {
    if (pending === null || prompt !== pending) return;
    setPending(null);
    formRef.current?.requestSubmit();
  }, [formRef, pending, prompt]);
  return (value: string) => { setPrompt(value); setPending(value); };
}
