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
  const text = (zhText: string, en: string) => zh ? zhText : en;
  const [target, setTarget] = useState<CollaborationEngine>(engine);
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [directoryMode, setDirectoryMode] = useState<"shared" | "worktree">("shared");
  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  useEffect(() => { if (open) { setTarget(engine); setModel(""); setEffort(""); setDirectoryMode("shared"); setTitle(""); setContent(""); } }, [open, engine, sessionID]);
  const state = useData<State>(open ? `/sessions/${engine}/${encodeURIComponent(sessionID)}/collaboration` : null, open);
  const codex = useCodexModels(open && target === "codex" ? workspaceID : undefined);
  const claude = useClaudeModels(open && target === "claude" ? workspaceID : "", sessionID);
  const gemini = useGeminiModels(open && target === "gemini" ? workspaceID : "");

  const sameEngine = target === engine;
  // Omitting the model inherits the caller's model only within the same engine.
  const defaultModel = sameEngine ? text(`跟随当前会话${currentModel ? `（${currentModel}）` : ""}`, `Same as this session${currentModel ? ` (${currentModel})` : ""}`) : text("服务器默认模型", "Server default model");
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
  const blocker = state.error ? state.error
    : !state.data ? ""
    : !state.data.supported ? text("请升级此服务器的 Agent 后使用协作工具。", "Update this server's Agent to use collaboration tools.")
    : !grant?.enabled ? text("当前会话未开启会话协作，请先在会话协作设置中允许模型创建和管理子会话。", "Collaboration is off for this session. Enable it in the collaboration settings first.")
    : !sameEngine && !grant.target_modes?.[target] ? text(`未授权创建 ${engineNames[target]} 会话，请先在会话协作设置中授予跨模型执行权限。`, `Creating ${engineNames[target]} sessions is not granted. Grant cross-model permissions in the collaboration settings first.`)
    : "";
  const worktreeAllowed = Boolean(grant?.worktree);
  useEffect(() => { if (!worktreeAllowed && directoryMode === "worktree") setDirectoryMode("shared"); }, [worktreeAllowed, directoryMode]);
  const ready = Boolean(state.data) && !blocker && Boolean(content.trim());
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!ready) return;
    onSubmit(collaborationTaskPrompt({ engine: target, model, effort, directoryMode, title, content }, zh));
  };
  return <Dialog open={open} title={text("创建协作会话", "Create collaboration session")} onClose={onClose} closeLabel={t("common.close")} wide>
    <form className="collaboration-task" onSubmit={submit}>
      <p>{text("选择的参数会原样交给当前会话的模型，由它调用协作工具创建新会话。", "These choices are passed verbatim to this session's model, which creates the new session with the collaboration tools.")}</p>
      <div className="form-grid">
        <label className="field"><span>{text("Agent 类型", "Agent")}</span><select aria-label={text("Agent 类型", "Agent")} value={target} onChange={event => { setTarget(event.target.value as CollaborationEngine); setModel(""); setEffort(""); }}>
          {(["codex", "claude", "gemini"] as const).map(value => <option key={value} value={value}>{engineNames[value]}{value === engine ? text("（当前）", " (current)") : ""}</option>)}
        </select></label>
        <label className="field"><span>{text("工作目录", "Working directory")}</span><select aria-label={text("工作目录", "Working directory")} value={directoryMode} onChange={event => setDirectoryMode(event.target.value as "shared" | "worktree")}>
          <option value="shared">{text("共享当前工作目录", "Share this working directory")}</option>
          <option value="worktree" disabled={!worktreeAllowed}>{text("独立 worktree", "Separate worktree")}{worktreeAllowed ? "" : text("（未授权）", " (not granted)")}</option>
        </select></label>
      </div>
      <div className="form-grid">
        <label className="field"><span>{text("模型", "Model")}</span><select aria-label={text("模型", "Model")} value={model} onChange={event => setModel(event.target.value)}>
          <option value="">{defaultModel}</option>
          {models.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select>{modelsLoading && <small className="muted" role="status">{text("正在获取可用模型…", "Loading available models…")}</small>}{modelsError && <small className="gemini-model-error" role="status">{text("获取模型列表失败：", "Could not load models: ")}{modelsError}</small>}</label>
        <label className="field"><span>{text("思考等级", "Thinking level")}</span><select aria-label={text("思考等级", "Thinking level")} value={effort} disabled={!efforts.length} onChange={event => setEffort(event.target.value)}>
          <option value="">{target === "gemini" && !model ? text("默认（Gemini 需先选择模型）", "Default (choose a Gemini model first)") : text("默认", "Default")}</option>
          {efforts.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select></label>
      </div>
      <label className="field"><span>{text("会话标题（可选）", "Session title (optional)")}</span><input aria-label={text("会话标题", "Session title")} value={title} maxLength={180} onChange={event => setTitle(event.target.value)} /></label>
      <label className="field"><span>{text("协作内容", "Task")}</span><textarea aria-label={text("协作内容", "Task")} rows={6} required maxLength={20000} value={content} placeholder={text("描述需要新会话处理的问题和要求", "Describe what the new session should handle")} onChange={event => setContent(event.target.value)} /></label>
      {state.loading && !state.data && <p className="muted" role="status">{t("common.loading")}</p>}
      {blocker && <p className="error-banner" role="alert">{blocker}</p>}
      <DialogActions><button type="button" className="secondary-button" onClick={onClose}>{t("common.cancel")}</button><button className="primary-button" disabled={!ready}><Network size={16} />{text("交给模型创建", "Send to model")}</button></DialogActions>
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
