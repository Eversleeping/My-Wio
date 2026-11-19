import { SubagentDetails, type NativeSubagent } from "../../../components/SubagentDetails";
import { useData, useThrottledValue } from "../../../useData";
import { useComposerDraft } from "../../composerDrafts";
import {
Activity,
AlertTriangle,
Archive,
ArrowUpFromLine,
Bot,
Boxes,
Braces,
ChevronDown,
ChevronRight,
Cpu,
Folder,
Gauge,
GitBranch,
LoaderCircle,
Minimize2,
Network,
Pause,
Pencil,
Play,
Plus,
RefreshCw,
Server as ServerIcon,
Square,
StickyNote,
Target,
Trash2,
Users,
Workflow,
X
} from "lucide-react";
import {
FormEvent,
KeyboardEvent as ReactKeyboardEvent,
useEffect,
useLayoutEffect,
useMemo,
useRef,
useState
} from "react";
import { api,APIError,post,put,remove } from "../../../api";
import { asyncQuestions,parseQuestionReply,questionReply } from "../../../asyncUserInput";
import {
loadCodexComposerPreferences,
saveCodexComposerPreferences,
type CodexComposerPreferences
} from "../../../codexComposerPreferences";
import { type FilePreviewSelection } from "../../../codexContent";
import { compatibleReasoningEffort,reasoningOptionsFor,useCodexModels } from "../../../codexModels";
import { CodexRequests } from "../../../codexRequests";
import { CodexModelPicker } from "../../../components/CodexModelPicker";
import { CollaborationTaskDialog,useSubmitPrompt } from "../../../components/CollaborationTaskDialog";
import { ComposerImagePicker } from "../../../components/ComposerImagePicker";
import { DialogActions } from "../../../components/Dialog";
import { Dialog,ErrorBanner,Field } from "../../../components/FormUI";
import { Empty,ErrorState,Status } from "../../../components/PageUI";
import { SessionUsage } from "../../../components/SessionUsage";
import { VirtualizedItems } from "../../../components/VirtualizedList";
import { formatDate } from "../../../format";
import { useI18n } from "../../../i18n";
import { compressImage } from "../../../imageCompression";
import { messageForkTargets } from "../../../messageForks";
import { SlashCommandMenu,type SlashCommandItem } from "../../../SlashCommandMenu";
import { message } from "../../../text";
import {
type Approval,
type CodexGoal,
type CodexMCPServer,
type CodexSkill,
type CodexSnapshot,
type CodexStatusData,
type StreamEvent,
type Thread,
type Workspace
} from "../../../types";
import { ApprovalPrompt } from "./approvals";
import {
asRecord,
collectSubagentActivity,
conversationEvents,
extractText,
groupCommandEvents,
normalizeGoal,
normalizeMCP,
normalizeSkills,
normalizeStatus,
type ConversationDisplayItem
} from "./conversation";
import { CommandEventGroup,ConversationEventItem,RawEventItem,WorkingIndicator } from "./events";
import { codexScrollPositions,isNearCodexStreamBottom,latestScrollTop,setScrollTopImmediately } from "./scroll";
import { useThreadEvents } from "./useThreadEvents";
import { waitForBackoff,waitForGoalSnapshot } from "./waiters";


type ComposerImage = { id: string; dataURL: string };

export function CreateThread({ workspaces, onCreated, initialWorkspaceID = "", fixedWorkspace = false, disabled = false, onBusyChange }: { onBusyChange?: (busy: boolean) => void; fixedWorkspace?: boolean; disabled?: boolean; workspaces: Workspace[]; initialWorkspaceID?: string; onCreated: (thread: Thread) => void }) {
  const { t } = useI18n();
  const [workspaceID, setWorkspaceID] = useState(initialWorkspaceID);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <form onSubmit={async e => { e.preventDefault(); if (busy || disabled || !workspaceID) return; setBusy(true); onBusyChange?.(true); setError(""); try { onCreated(await post<Thread>("/sessions/codex", { workspace_id: workspaceID })); } catch (requestError) { setError(message(requestError)); } finally { setBusy(false); onBusyChange?.(false); } }}>{error && <ErrorBanner text={error} />}{!fixedWorkspace && <Field label={t("codex.workspace")}><select value={workspaceID} disabled={busy || disabled} onChange={e => setWorkspaceID(e.target.value)} required><option value="">{t("codex.selectWorkspaceOption")}</option>{workspaces.map(workspace => <option value={workspace.id} key={workspace.id}>{workspace.project_name} · {workspace.server_name} · {workspace.path}</option>)}</select></Field>}<DialogActions><button className="primary-button" disabled={busy || disabled}>{busy ? <LoaderCircle className="spin" size={16} /> : <Plus size={16} />}{t("codex.createSession")}</button></DialogActions></form>;
}

export function SessionView({ thread, approvals, realtime, globalStreamRevision = 0, streamRevision = 0, invalidationSequence, reloadApprovals, notify, onOpenFile, onNewTask, onFork, forkBusy = false }: { thread: Thread; approvals: Approval[]; realtime: unknown; globalStreamRevision?: number; streamRevision?: number; invalidationSequence?: number | null; reloadApprovals: () => void; notify: (text: string) => void; onOpenFile: (selection: FilePreviewSelection) => void; onNewTask: () => void; onFork?: (eventID: string) => Promise<void>; forkBusy?: boolean }) {
  const { t } = useI18n();
  const [rawEvents, setRawEvents] = useState(false);
  const events = useThreadEvents(thread.id, rawEvents, realtime, globalStreamRevision, streamRevision, invalidationSequence);
  const { prompt, setPrompt, images, setImages, editingEventID, setEditingEventID, composerMode, setComposerMode } = useComposerDraft("codex", thread.id);
  const [imageBusy, setImageBusy] = useState(false);
  const imageCompressionRef = useRef<AbortController | null>(null);
  const [sending, setSending] = useState(false);
  const codexRequests = useRef(new CodexRequests());
  const [interrupting, setInterrupting] = useState(false);
  const [composerPreferences, setComposerPreferences] = useState(() => loadCodexComposerPreferences(thread.id));
  const { approvalMode, model, reasoningEffort } = composerPreferences;
  const updateComposerPreferences = (changes: Partial<CodexComposerPreferences>) => {
    setComposerPreferences(current => {
      const next = { ...current, ...changes };
      saveCodexComposerPreferences(thread.id, next);
      return next;
    });
  };
  const setModel = (value: string) => updateComposerPreferences({ model: value });
  const setReasoningEffort = (value: string) => updateComposerPreferences({ reasoningEffort: value });
  const setApprovalMode = (value: string) => updateComposerPreferences({ approvalMode: value });
  const [customModelSignal, setCustomModelSignal] = useState(0);
  const [slashMode, setSlashMode] = useState<"commands" | "model" | "reasoning">("commands");
  const [slashDismissedValue, setSlashDismissedValue] = useState("");
  const [statusVisible, setStatusVisible] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const [skillsOpen, setSkillsOpen] = useState(false);
  const [planOpen, setPlanOpen] = useState(false);
  const [collabOpen, setCollabOpen] = useState(false);
  const composerFormRef = useRef<HTMLFormElement>(null);
  const submitPrompt = useSubmitPrompt(composerFormRef, prompt, setPrompt);
  const [subagentsOpen, setSubagentsOpen] = useState(false);
  const [selectedSubagent, setSelectedSubagent] = useState<NativeSubagent | null>(null);
  const [compactBusy, setCompactBusy] = useState(false);
  const [statusSnapshot, setStatusSnapshot] = useState<CodexSnapshot<CodexStatusData> | null>(null);
  const [mcpSnapshot, setMcpSnapshot] = useState<CodexSnapshot<CodexMCPServer[]> | null>(null);
  const [skillsSnapshot, setSkillsSnapshot] = useState<CodexSnapshot<CodexSkill[]> | null>(null);
  const [goal, setGoal] = useState<CodexGoal | null>(null);
  const [nativeBusy, setNativeBusy] = useState("");
  const [nativeError, setNativeError] = useState("");
  const goalRequestRef = useRef(0);
  // Aborted when the session view changes thread or unmounts, ending native polling.
  const nativeRequestsRef = useRef<AbortController | null>(null);
  const streamRef = useRef<HTMLDivElement>(null);
  const restoredScrollKeyRef = useRef("");
  const autoFollowStreamRef = useRef(true);
  const eventCountRef = useRef<{ key: string; count: number } | null>(null);
  const historyAnchorRef = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);
  const [hasNewEventsBelow, setHasNewEventsBelow] = useState(false);
  const promptRef = useRef<HTMLTextAreaElement>(null);
  const slashKeyboardRef = useRef<((event: ReactKeyboardEvent<HTMLTextAreaElement>) => boolean) | null>(null);
  const sourceEvents = events.data ?? [];
  const [submittedAnswers, setSubmittedAnswers] = useState<Record<string, string>>({});
  const questionAnswers = useMemo(() => {
    const answers = { ...submittedAnswers };
    for (const event of sourceEvents) {
      const payload = asRecord(event.payload);
      const item = asRecord(payload?.item);
      const text = event.kind === "user.message" ? String(payload?.text ?? "") : item?.type === "userMessage" ? extractText(item) : "";
      for (const reply of parseQuestionReply(text)) answers[reply.questionItemId] = reply.answer;
    }
    return answers;
  }, [sourceEvents, submittedAnswers]);
  const chatEvents = useMemo(() => conversationEvents(sourceEvents), [sourceEvents]);
  const forkTargets = useMemo(() => messageForkTargets(sourceEvents, chatEvents), [sourceEvents, chatEvents]);
  const displayItems = useMemo(() => groupCommandEvents(chatEvents), [chatEvents]);
  const legacySubagents = useMemo(() => collectSubagentActivity(sourceEvents), [sourceEvents]);
  const subagentRevision = useThrottledValue(realtime, 500);
  const observedSubagents = useData<{ items: NativeSubagent[] }>(`/sessions/codex/${encodeURIComponent(thread.id)}/subagents`, subagentRevision);
  const subagents = observedSubagents.data?.items?.map(agent => ({
    threadID: agent.id, path: agent.name || "", status: agent.status || "unknown",
    prompt: agent.prompt || "", message: agent.summary || "", model: agent.model || "",
    reasoningEffort: legacySubagents.find(item => item.threadID === agent.id)?.reasoningEffort || "", updatedAt: agent.updated_at
  })) ?? legacySubagents;
  const activeSubagents = subagents.filter(agent => ["pendingInit", "queued", "running"].includes(agent.status));
  const activeTurn = thread.status === "queued" || thread.status === "running";
  const steering = thread.status === "running" && composerMode === "message" && !editingEventID;
  const composerBlocked = activeTurn && !steering;
  const goalRuntimeStatus = goal ? (["paused", "blocked", "usageLimited", "budgetLimited", "complete"].includes(goal.status) ? goal.status : thread.status) : "idle";
  const slashCandidate = /^\/[^\r\n]*$/.test(prompt);
  const slashOpen = composerMode === "message" && !editingEventID && slashCandidate && prompt !== slashDismissedValue;
  const slashQuery = slashMode === "commands" ? prompt.slice(1) : prompt.replace(/^\/(?:model|reasoning)\s*/, "");
  const closeSlash = () => { setSlashDismissedValue(prompt); setSlashMode("commands"); };
  const finishSlash = (action: () => void) => { setPrompt(""); setSlashDismissedValue(""); setSlashMode("commands"); action(); requestAnimationFrame(() => promptRef.current?.focus()); };
  const enterGoalMode = () => {
    setComposerMode("goal");
    setEditingEventID("");
    setImages([]);
    setPrompt(goal?.objective ?? "");
  };
  const leaveGoalMode = () => {
    setComposerMode("message");
    setPrompt("");
  };
  const loadSnapshot = async <T,>(kind: "status" | "mcp" | "skills", refresh = false) => {
    const signal = nativeRequestsRef.current?.signal;
    if (!signal || signal.aborted) return;
    const workspacePath = `/workspaces/${thread.workspace_id}/codex/${kind}`;
    const path = kind === "status" ? `/sessions/codex/${thread.id}/status` : workspacePath;
    const setter = kind === "status" ? setStatusSnapshot : kind === "mcp" ? setMcpSnapshot : setSkillsSnapshot;
    setNativeBusy(kind); setNativeError("");
    try {
      let next = await api<CodexSnapshot<unknown>>(path, { signal });
      const shouldRefresh = refresh || next.status === "idle";
      if (shouldRefresh) { await post(`${path}/refresh`, {}, { signal }); next = await api<CodexSnapshot<unknown>>(path, { signal }); }
      for (let attempt = 0; shouldRefresh && next.status === "loading" && attempt < 20; attempt++) { await waitForBackoff(attempt, undefined, undefined, signal); next = await api<CodexSnapshot<unknown>>(path, { signal }); }
      const normalized = { ...next, data: kind === "skills" ? normalizeSkills(next.data) : kind === "mcp" ? normalizeMCP(next.data) : normalizeStatus(next.data) } as CodexSnapshot<T>;
      setter(normalized as never);
    }
    catch (error) { if (!signal.aborted) setNativeError(message(error)); }
    finally { if (!signal.aborted) setNativeBusy(""); }
  };
  const loadGoal = async (refresh = false) => {
    const signal = nativeRequestsRef.current?.signal;
    if (!signal || signal.aborted) return;
    const requestID = ++goalRequestRef.current;
    const current = () => requestID === goalRequestRef.current && !signal.aborted;
    setNativeBusy("goal"); setNativeError("");
    try {
      let snapshot = await api<CodexSnapshot<unknown>>(`/sessions/codex/${thread.id}/goal`, { signal });
      if (refresh) {
        await post(`/sessions/codex/${thread.id}/goal/refresh`, {}, { signal });
        snapshot = await waitForGoalSnapshot(thread.id, undefined, signal) ?? snapshot;
      }
      if (!current()) return;
      const next = normalizeGoal(snapshot.data);
      setGoal(next);
      if (!snapshot.supported) setNativeError(snapshot.reason || t("codex.unsupported"));
    }
    catch (error) { if (current()) setNativeError(message(error)); }
    finally { if (current()) setNativeBusy(""); }
  };
  const syncGoalSnapshot = async (expected?: { objective?: string; status?: string; cleared?: boolean }) => {
    const signal = nativeRequestsRef.current?.signal;
    if (!signal || signal.aborted) return;
    const requestID = ++goalRequestRef.current;
    try {
      const snapshot = await waitForGoalSnapshot(thread.id, expected, signal);
      if (!snapshot || requestID !== goalRequestRef.current || signal.aborted) return;
      if (!snapshot.supported || snapshot.status === "failed" || snapshot.status === "unsupported") {
        setNativeError(snapshot.reason || snapshot.error || t("codex.unsupported"));
        return;
      }
      const next = normalizeGoal(snapshot.data);
      if (expected?.cleared && next) return;
      if (expected?.objective && (!next || next.objective !== expected.objective || (expected.status && next.status !== expected.status))) return;
      setGoal(next);
      if (!snapshot.supported) setNativeError(snapshot.reason || t("codex.unsupported"));
    } catch (error) {
      if (requestID === goalRequestRef.current && !signal.aborted) setNativeError(message(error));
    }
  };
  const queueGoalTurn = async (objective: string) => {
    const content = { prompt: objective, model, reasoning_effort: reasoningEffort, approval_mode: approvalMode };
    await codexRequests.current.send("goal", [thread.id, content], `/sessions/codex/${thread.id}/turns`, content);
  };
  const saveGoal = async (objective: string, status: string, tokenBudget: number | null, resume = false) => {
    ++goalRequestRef.current;
    const creating = !goal;
    const needsInitialTurn = creating && status === "active";
    const initialCodexThreadID = thread.codex_thread_id;
    if (creating && !initialCodexThreadID && status !== "active") throw new Error(t("codex.goalNeedsActive"));
    await put(`/sessions/codex/${thread.id}/goal`, { objective, status, token_budget: tokenBudget });
    if (needsInitialTurn || (resume && status === "active" && !activeTurn)) await queueGoalTurn(objective);
    const optimistic: CodexGoal = { thread_id: initialCodexThreadID, objective, status, token_budget: tokenBudget, tokens_used: goal?.tokens_used ?? 0, time_used_seconds: goal?.time_used_seconds ?? 0, created_at: goal?.created_at ?? Math.floor(Date.now() / 1000), updated_at: Math.floor(Date.now() / 1000) };
    setGoal(optimistic);
    void syncGoalSnapshot({ objective, status });
  };
  const compactContext = async () => {
    if (compactBusy) return;
    if (activeTurn) { notify(t("codex.waitForTurn")); return; }
    setCompactBusy(true);
    try {
      await post(`/sessions/codex/${thread.id}/compact`, {});
      notify(t("codex.compactQueued"));
    } catch (error) {
      notify(message(error));
    } finally {
      setCompactBusy(false);
    }
  };
  const updateGoalStatus = async (status: "active" | "paused") => {
    if (!goal || nativeBusy === "goal") return;
    setNativeBusy("goal"); setNativeError("");
    try {
      if (status === "paused" && activeTurn) {
        try {
          await post(`/sessions/codex/${thread.id}/interrupt`, {});
        } catch (error) {
          if (!(error instanceof APIError && error.status === 409)) throw error;
        }
      }
      await saveGoal(goal.objective, status, goal.token_budget, status === "active");
      notify(t(status === "active" ? "codex.goalResumed" : "codex.goalPaused"));
    } catch (error) {
      notify(message(error));
    } finally {
      setNativeBusy("");
    }
  };
  const clearCurrentGoal = async () => {
    if (!goal || nativeBusy === "goal") return;
    ++goalRequestRef.current;
    setNativeBusy("goal"); setNativeError("");
    try {
      if (activeTurn) {
        try {
          await post(`/sessions/codex/${thread.id}/interrupt`, {});
        } catch (error) {
          if (!(error instanceof APIError && error.status === 409)) throw error;
        }
      }
      await remove(`/sessions/codex/${thread.id}/goal`);
      setGoal(null);
      if (composerMode === "goal") leaveGoalMode();
      void syncGoalSnapshot({ cleared: true });
      notify(t("codex.goalCleared"));
    } catch (error) {
      setNativeError(message(error));
      notify(message(error));
    } finally {
      setNativeBusy("");
    }
  };
  const catalogue = useCodexModels(thread.workspace_id);
  const codexReasoningOptions = reasoningOptionsFor(catalogue.models, model);
  useEffect(() => {
    const next = compatibleReasoningEffort(catalogue.models, model, reasoningEffort);
    if (next !== reasoningEffort) setReasoningEffort(next);
  }, [catalogue.models, model, reasoningEffort]);
  const modelItems: SlashCommandItem[] = [
    { id: "default", name: t("codex.modelServerDefault"), description: t("codex.slashModelDefaultDescription"), icon: Cpu, selected: model === "", onSelect: () => finishSlash(() => setModel("")) },
    ...catalogue.models.map(option => ({ id: option.model, name: option.displayName || option.model, description: option.description || option.model, icon: Cpu, selected: model === option.model, onSelect: () => finishSlash(() => setModel(option.model)) }))
  ];
  if (model && !catalogue.models.some(option => option.model === model)) modelItems.push({ id: model, name: model, description: t("codex.slashCurrentCustomModel"), icon: Cpu, selected: true, onSelect: () => finishSlash(() => setModel(model)) });
  modelItems.push({ id: "custom", name: t("codex.modelCustom"), description: t("codex.slashCustomModelDescription"), icon: Cpu, onSelect: () => finishSlash(() => { setModel(""); setCustomModelSignal(value => value + 1); }) });
  const reasoningItems: SlashCommandItem[] = [
    { id: "default", name: t("codex.reasoningDefault"), description: t("codex.slashReasoningDefaultDescription"), icon: Gauge, selected: reasoningEffort === "", onSelect: () => finishSlash(() => setReasoningEffort("")) },
    ...codexReasoningOptions.map(option => ({ id: option.value, name: option.labelKey ? t(option.labelKey) : option.value, description: option.value, icon: Gauge, selected: reasoningEffort === option.value, onSelect: () => finishSlash(() => setReasoningEffort(option.value)) }))
  ];
  const commandItems: SlashCommandItem[] = [
    { id: "model", name: "/model", description: t("codex.slashModelDescription"), detail: model || t("codex.modelServerDefault"), icon: Cpu, onSelect: () => { setPrompt("/model "); setSlashMode("model"); } },
    { id: "reasoning", name: "/reasoning", description: t("codex.slashReasoningDescription"), detail: reasoningEffort ? t(codexReasoningOptions.find(option => option.value === reasoningEffort)?.labelKey ?? "codex.reasoningDefault") : t("codex.reasoningDefault"), icon: Gauge, onSelect: () => { setPrompt("/reasoning "); setSlashMode("reasoning"); } },
    { id: "status", name: "/status", description: t("codex.slashStatusDescription"), icon: Activity, onSelect: () => finishSlash(() => { setStatusVisible(true); void loadSnapshot<CodexStatusData>("status", true); }) },
    { id: "goal", name: "/goal", description: t("codex.slashGoalDescription"), icon: Target, onSelect: () => finishSlash(enterGoalMode) },
    { id: "compact", name: "/compact", description: t("codex.slashCompactDescription"), icon: Minimize2, onSelect: () => finishSlash(() => void compactContext()) },
    { id: "subagents", name: "/subagents", description: t("codex.slashSubagentsDescription"), detail: subagents.length ? String(subagents.length) : undefined, icon: Users, onSelect: () => finishSlash(() => setSubagentsOpen(true)) },
    { id: "mcp", name: "/mcp", description: t("codex.slashMCPDescription"), icon: Network, onSelect: () => finishSlash(() => { setMcpOpen(true); void loadSnapshot<CodexMCPServer[]>("mcp"); }) },
    { id: "skills", name: "/skills", description: t("codex.slashSkillsDescription"), icon: Boxes, onSelect: () => finishSlash(() => { setSkillsOpen(true); void loadSnapshot<CodexSkill[]>("skills"); }) },
    { id: "plan", name: "/plan", description: t("codex.slashPlanDescription"), detail: t("codex.unsupported"), icon: StickyNote, onSelect: () => finishSlash(() => setPlanOpen(true)) },
    { id: "project", name: "/project", description: t("codex.slashProjectDescription"), icon: Folder, onSelect: () => finishSlash(onNewTask) },
    { id: "collab", name: "/collab", description: t("codex.slashCollabDescription"), icon: Workflow, onSelect: () => finishSlash(() => setCollabOpen(true)) }
  ];
  const skillItems: SlashCommandItem[] = (skillsSnapshot?.data ?? []).filter(skill => skill.enabled).map(skill => ({ id: `skill:${skill.name}`, name: `$${skill.name}`, description: skill.short_description || skill.description, detail: skill.scope, section: t("codex.availableSkills"), icon: Boxes, onSelect: () => finishSlash(() => { setPrompt(`$${skill.name} `); requestAnimationFrame(() => { promptRef.current?.focus(); promptRef.current?.setSelectionRange(skill.name.length + 2, skill.name.length + 2); }); }) }));
  const slashItems = slashMode === "model" ? modelItems : slashMode === "reasoning" ? reasoningItems : [...commandItems, ...skillItems];
  useEffect(() => {
    const controller = new AbortController();
    nativeRequestsRef.current = controller;
    ++goalRequestRef.current; setRawEvents(false); setStatusVisible(false); setGoal(null); setStatusSnapshot(null); setMcpSnapshot(null); setSkillsSnapshot(null); setSubagentsOpen(false); setNativeBusy(""); void loadGoal();
    return () => { controller.abort(); if (nativeRequestsRef.current === controller) nativeRequestsRef.current = null; };
  }, [thread.id]);
  useEffect(() => { if (slashOpen && slashMode === "commands" && !skillsSnapshot && nativeBusy !== "skills") void loadSnapshot<CodexSkill[]>("skills"); }, [slashOpen, slashMode, thread.workspace_id]);
  const scrollStateKey = `${thread.id}:${rawEvents ? "raw" : "conversation"}`;
  const eventsReady = events.data !== null;
  const scrollToLatestEvent = () => {
    const stream = streamRef.current;
    if (!stream) return;
    setScrollTopImmediately(stream, latestScrollTop(stream));
    codexScrollPositions.set(scrollStateKey, stream.scrollTop);
    autoFollowStreamRef.current = true;
    setHasNewEventsBelow(false);
  };
  const loadEarlierEvents = async () => {
    const stream = streamRef.current;
    if (!stream) return;
    historyAnchorRef.current = { scrollHeight: stream.scrollHeight, scrollTop: stream.scrollTop };
    if (!await events.loadEarlier()) historyAnchorRef.current = null;
  };
  useLayoutEffect(() => {
    const stream = streamRef.current;
    if (!stream || !eventsReady) return;
    if (restoredScrollKeyRef.current !== scrollStateKey) {
      setScrollTopImmediately(stream, codexScrollPositions.get(scrollStateKey) ?? latestScrollTop(stream));
      restoredScrollKeyRef.current = scrollStateKey;
      autoFollowStreamRef.current = isNearCodexStreamBottom(stream);
      eventCountRef.current = { key: scrollStateKey, count: sourceEvents.length };
      setHasNewEventsBelow(false);
      return;
    }
    const previous = eventCountRef.current;
    const hasAppendedEvents = previous?.key === scrollStateKey && sourceEvents.length > previous.count;
    eventCountRef.current = { key: scrollStateKey, count: sourceEvents.length };
    const historyAnchor = historyAnchorRef.current;
    if (historyAnchor) {
      historyAnchorRef.current = null;
      setScrollTopImmediately(stream, historyAnchor.scrollTop + stream.scrollHeight - historyAnchor.scrollHeight);
      codexScrollPositions.set(scrollStateKey, stream.scrollTop);
      autoFollowStreamRef.current = isNearCodexStreamBottom(stream);
      return;
    }
    if (!hasAppendedEvents) return;
    if (autoFollowStreamRef.current) {
      setScrollTopImmediately(stream, latestScrollTop(stream));
      codexScrollPositions.set(scrollStateKey, stream.scrollTop);
      setHasNewEventsBelow(false);
      return;
    }
    setHasNewEventsBelow(true);
  }, [eventsReady, scrollStateKey, sourceEvents.length]);
  useLayoutEffect(() => {
    const stream = streamRef.current;
    return () => { if (stream && restoredScrollKeyRef.current === scrollStateKey) codexScrollPositions.set(scrollStateKey, stream.scrollTop); };
  }, [scrollStateKey]);
  useEffect(() => {
    setImageBusy(false);
    return () => {
      imageCompressionRef.current?.abort();
      imageCompressionRef.current = null;
    };
  }, [thread.id]);
  const addImages = async (files: File[]) => {
    const available = 4 - images.length;
    if (available <= 0) { notify(t("codex.imageLimit")); return; }
    imageCompressionRef.current?.abort();
    const controller = new AbortController();
    imageCompressionRef.current = controller;
    setImageBusy(true);
    try {
      const selected = files.slice(0, available);
      const prepared = new Array<ComposerImage>(selected.length);
      let nextIndex = 0;
      const prepareNext = async () => {
        while (nextIndex < selected.length) {
          const index = nextIndex++;
          prepared[index] = { id: crypto.randomUUID(), dataURL: await compressImage(selected[index], { signal: controller.signal }) };
        }
      };
      await Promise.all(Array.from({ length: Math.min(2, selected.length) }, () => prepareNext()));
      setImages(current => [...current, ...prepared].slice(0, 4));
      if (files.length > available) notify(t("codex.imageLimit"));
    } catch {
      const wasCancelled = controller.signal.aborted;
      controller.abort();
      if (!wasCancelled) notify(t("codex.imageFailed"));
    } finally {
      if (imageCompressionRef.current === controller) {
        imageCompressionRef.current = null;
        setImageBusy(false);
      }
    }
  };
  const composerHasContent = composerMode === "goal" ? Boolean(prompt.trim()) : Boolean(prompt.trim() || images.length > 0);
  const showInterrupt = activeTurn && (interrupting || !composerHasContent || composerBlocked);
  const send = async (event: FormEvent) => {
    event.preventDefault();
    if (slashOpen || !composerHasContent || imageBusy || sending || interrupting) return;
    if (composerBlocked) { notify(t("codex.waitForTurn")); return; }
    setSending(true);
    try {
      if (composerMode === "goal") {
        const creating = !goal;
        setNativeBusy("goal");
        setNativeError("");
        await saveGoal(prompt.trim(), "active", goal?.token_budget ?? null, Boolean(goal && goal.status !== "active"));
        setComposerMode("message");
        setPrompt("");
        notify(t(creating ? "codex.goalStarted" : "codex.goalSaved"));
        return;
      }
      const turnPath = steering ? `/sessions/codex/${thread.id}/steer` : editingEventID ? `/sessions/codex/${thread.id}/events/${editingEventID}/rewrite` : `/sessions/codex/${thread.id}/turns`;
      const content = { prompt, images: images.map(image => ({ data_url: image.dataURL })) };
      const options = { ...content, model, reasoning_effort: reasoningEffort, approval_mode: approvalMode };
      if (editingEventID) await post(turnPath, options);
      else await codexRequests.current.send("composer", [thread.id, options], turnPath, steering ? content : options);
      setPrompt(""); setImages([]); setEditingEventID(""); notify(t(steering ? "codex.steerQueued" : editingEventID ? "codex.rewriteQueued" : "codex.turnQueued"));
    } catch (err) {
      const steerErrorKeys: Record<string, string> = { steer_inactive: "codex.steerInactive", steer_offline: "codex.steerOffline", steer_agent_update: "codex.steerAgentUpdate" };
      notify(err instanceof APIError && steerErrorKeys[err.code] ? t(steerErrorKeys[err.code]) : message(err));
    } finally { if (composerMode === "goal") setNativeBusy(""); setSending(false); }
  };
  const editMessage = (eventID: string, text: string) => {
    setComposerMode("message");
    setPrompt(text);
    setImages([]);
    setEditingEventID(eventID);
    requestAnimationFrame(() => {
      promptRef.current?.focus();
      promptRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
      promptRef.current?.setSelectionRange(text.length, text.length);
    });
    notify(t("codex.messageReadyToEdit"));
  };
  const interrupt = async () => {
    if (interrupting) return;
    setInterrupting(true);
    try {
      await post(`/sessions/codex/${thread.id}/interrupt`, {});
      notify(t("codex.interruptQueued"));
    } catch (error) {
      notify(message(error));
    } finally {
      setInterrupting(false);
    }
  };
  const answerQuestion = async (event: StreamEvent, answers: Record<string, string>) => {
    const questions = asyncQuestions(asRecord(asRecord(event.payload)?.item));
    const content = { prompt: questionReply(questions, answers), images: [] };
    const current = await api<Thread>(`/sessions/codex/${thread.id}`);
    const options = { ...content, model, reasoning_effort: reasoningEffort, approval_mode: approvalMode };
    const sendAnswer = (path: string, body: Record<string, unknown>) => codexRequests.current.send(`question:${event.event_id}`, [thread.id, options], path, body);
    const start = () => sendAnswer(`/sessions/codex/${thread.id}/turns`, options);
    if (current.status === "running") {
      try { await sendAnswer(`/sessions/codex/${thread.id}/steer`, content); }
      catch (error) {
        // Only retry a request explicitly rejected before it was queued.
        if (!(error instanceof APIError) || error.code !== "steer_inactive") throw error;
        await start();
      }
    } else await start();
    setSubmittedAnswers(previous => ({ ...previous, ...answers }));
    events.reload();
  };
  const renderDisplayItem = (item: ConversationDisplayItem) => item.type === "commandGroup"
    ? <CommandEventGroup events={item.events} />
    : <ConversationEventItem event={item.event} onEdit={thread.archived_at ? undefined : editMessage} onFork={!thread.archived_at && thread.codex_thread_id && forkTargets.has(item.event.event_id) ? onFork : undefined} forkDisabled={forkBusy || thread.status === "running" || thread.status === "queued"} notify={notify} workspaceRoot={thread.path} onOpenFile={onOpenFile} questionAnswers={questionAnswers} onAnswer={thread.archived_at ? undefined : values => answerQuestion(item.event, values)} />;
  const statusPanel = statusVisible ? <section className="composer-status-panel" role="region" aria-label={t("codex.taskStatus")}>
    <header><strong>{t("codex.taskStatus")}</strong><div><button type="button" className="text-button" disabled={nativeBusy === "status"} onClick={() => void loadSnapshot<CodexStatusData>("status", true)}>{nativeBusy === "status" ? <LoaderCircle className="spin" size={14} /> : <RefreshCw size={14} />}{t("common.refresh")}</button><button type="button" className="text-button" onClick={() => setStatusVisible(false)}>{t("common.close")}</button></div></header>
    <SnapshotNotice snapshot={statusSnapshot} loading={nativeBusy === "status"} error={nativeError} />
    <dl className="composer-status-list"><div><dt>{t("codex.statusCodexThreadID")}</dt><dd>{thread.codex_thread_id ? <code>{thread.codex_thread_id}</code> : t("codex.notBound")}</dd></div><div><dt>{t("codex.modelOverride")}</dt><dd>{String(statusSnapshot?.data?.model || model || t("codex.modelServerDefault"))}</dd></div><div><dt>{t("codex.reasoningEffort")}</dt><dd>{String(statusSnapshot?.data?.reasoning_effort || (reasoningEffort ? t(codexReasoningOptions.find(option => option.value === reasoningEffort)?.labelKey ?? "codex.reasoningDefault") : t("codex.reasoningDefault")))}</dd></div><div><dt>{t("codex.statusApprovalPolicy")}</dt><dd>{String(statusSnapshot?.data?.approval_policy || t(approvalMode === "on-request" ? "codex.approveOnRequest" : approvalMode === "untrusted" ? "codex.untrusted" : "codex.neverApprove"))}</dd></div><div><dt>{t("column.state")}</dt><dd><Status value={thread.status} /></dd></div>{statusSnapshot?.data?.account_type && <div><dt>{t("codex.statusAccount")}</dt><dd>{statusSnapshot.data.account_type}</dd></div>}{statusSnapshot?.data?.rate_limits_available === false && <div><dt>{t("codex.statusRateLimits")}</dt><dd>{t("codex.statusRateLimitsUnavailable")}</dd></div>}{(statusSnapshot?.data?.rate_limits ?? []).map(limit => <div key={limit.name}><dt>{limit.name}</dt><dd>{limit.used_percent == null ? limit.detail || "-" : `${limit.used_percent}%${limit.resets_at ? ` · ${t("codex.resetsAt", { time: formatDate(limit.resets_at) })}` : ""}`}</dd></div>)}</dl>
  </section> : null;
  return <>
    <div className="session-header"><div className="session-heading"><h2>{thread.title}</h2><span className="session-meta" title={`${thread.project_name} · ${thread.server_name}`}><GitBranch size={13} />{thread.project_name}<i /> <ServerIcon size={13} />{thread.server_name}</span></div><div className="session-actions"><button className={`icon-button ${rawEvents ? "active" : ""}`} aria-pressed={rawEvents} title={rawEvents ? t("codex.showConversation") : t("codex.showRawEvents")} onClick={() => setRawEvents(value => !value)}><Braces size={16} /></button><Status value={thread.status} /></div><SessionUsage provider="codex" threadID={thread.id} revision={`${realtime}:${globalStreamRevision}`} running={activeTurn} /></div>
    <div className={`event-stream ${rawEvents ? "raw-stream" : "conversation-stream"}`} ref={streamRef} aria-live="polite" onScroll={event => { const stream = event.currentTarget; autoFollowStreamRef.current = isNearCodexStreamBottom(stream); if (autoFollowStreamRef.current) setHasNewEventsBelow(false); if (restoredScrollKeyRef.current === scrollStateKey) codexScrollPositions.set(scrollStateKey, stream.scrollTop); }}>
      {events.loading ? <div className="page-loading"><LoaderCircle className="spin" size={20} /></div> : events.error && !events.data ? <ErrorState error={events.error} reload={events.reload} /> : <>
        {events.hasEarlier && <div className="history-loader"><button type="button" className="secondary-button small" disabled={events.loadingEarlier} onClick={() => void loadEarlierEvents()}>{events.loadingEarlier ? <LoaderCircle className="spin" size={15} /> : <ArrowUpFromLine size={15} />}{t(events.loadingEarlier ? "codex.loadingEarlier" : "codex.loadEarlier")}</button></div>}
        {events.error && events.data && <div className="snapshot-notice warning"><AlertTriangle size={15} />{events.error}</div>}
        {chatEvents.length === 0 && approvals.length === 0 && thread.status !== "running" ? <Empty icon={<Bot size={26} />} text={t("codex.noMessages")} /> : <>
          {rawEvents
            ? sourceEvents.length > 0 && <VirtualizedItems<StreamEvent> items={sourceEvents} scrollRef={streamRef} getKey={event => event.event_id} estimateSize={116} renderItem={event => <RawEventItem event={event} />} />
            : displayItems.length > 0 && <VirtualizedItems<ConversationDisplayItem> items={displayItems} scrollRef={streamRef} getKey={item => item.type === "commandGroup" ? `commands:${item.events[0].event_id}` : item.event.event_id} estimateSize={item => item.type === "commandGroup" ? 112 : 156} renderItem={renderDisplayItem} />}
          {approvals.map(item => <ApprovalPrompt key={item.id} item={item} onDecided={reloadApprovals} notify={notify} />)}
          {thread.status === "running" && approvals.length === 0 && <WorkingIndicator events={sourceEvents} />}
        </>}</>
      }
    </div>
    {thread.archived_at ? <div className="snapshot-notice"><Archive size={16} />{t("codex.archivedReadOnly")}</div> : <div className="composer-stack">{statusPanel}<form ref={composerFormRef} className={`composer ${composerMode === "goal" ? "goal-mode" : ""}`} onSubmit={send}>
      {hasNewEventsBelow && <button type="button" className="secondary-button" aria-label={t("codex.jumpToLatestMessages")} onClick={scrollToLatestEvent}><ChevronDown size={16} />{t("codex.newMessages")}</button>}
      {subagents.length > 0 && <button type="button" className="subagent-progress-row" onClick={() => setSubagentsOpen(true)}><Users size={16} /><span><strong>{t("codex.subagentActivity")}</strong><small>{activeSubagents.length > 0 ? t("codex.subagentsRunning", { count: activeSubagents.length }) : t("codex.subagentsRecorded", { count: subagents.length })}</small></span><ChevronRight size={15} /></button>}
      {goal && composerMode !== "goal" && <div className="goal-progress-row"><Target size={16} /><span title={goal.objective}><strong>{goal.objective}</strong><small>{t("codex.goalUsage", { tokens: goal.tokens_used, seconds: goal.time_used_seconds })}</small></span><Status value={goalRuntimeStatus} /><div>{goal.status === "active" ? <button type="button" className="icon-button" disabled={nativeBusy === "goal"} title={t("codex.pauseGoal")} aria-label={t("codex.pauseGoal")} onClick={() => void updateGoalStatus("paused")}>{nativeBusy === "goal" ? <LoaderCircle className="spin" size={14} /> : <Pause size={14} />}</button> : <button type="button" className="icon-button" disabled={nativeBusy === "goal"} title={t("codex.resumeGoal")} aria-label={t("codex.resumeGoal")} onClick={() => void updateGoalStatus("active")}>{nativeBusy === "goal" ? <LoaderCircle className="spin" size={14} /> : <Play size={14} />}</button>}<button type="button" className="icon-button" title={t("codex.editGoal")} aria-label={t("codex.editGoal")} onClick={enterGoalMode}><Pencil size={14} /></button><button type="button" className="icon-button danger" disabled={nativeBusy === "goal"} title={t("codex.clearGoal")} aria-label={t("codex.clearGoal")} onClick={() => void clearCurrentGoal()}><Trash2 size={14} /></button></div></div>}
      {editingEventID && <div className="composer-editing"><Pencil size={14} /><span>{t("codex.editingMessage")}</span><button type="button" className="icon-button" title={t("codex.cancelEdit")} aria-label={t("codex.cancelEdit")} onClick={() => { setEditingEventID(""); setPrompt(""); setImages([]); }}><X size={14} /></button></div>}
      {images.length > 0 && <div className="composer-images">{images.map(image => <figure key={image.id}><img src={image.dataURL} alt="" /><button type="button" title={t("common.close")} onClick={() => setImages(current => current.filter(item => item.id !== image.id))}><X size={13} /></button></figure>)}</div>}
      {slashOpen && <SlashCommandMenu items={slashItems} query={slashQuery} label={t("codex.slashMenu")} backLabel={t("codex.slashBackToCommands")} onBack={slashMode === "commands" ? undefined : () => { setPrompt("/"); setSlashMode("commands"); }} onDismiss={closeSlash} keyboardRef={slashKeyboardRef} />}
      <div className="composer-input">{composerMode !== "goal" && <ComposerImagePicker disabled={sending || interrupting || imageBusy || composerBlocked || images.length >= 4} onSelect={addImages} />}<textarea ref={promptRef} value={prompt} onChange={event => { setPrompt(event.target.value); if (event.target.value !== slashDismissedValue) setSlashDismissedValue(""); if (!event.target.value.startsWith("/model ") && !event.target.value.startsWith("/reasoning ")) setSlashMode("commands"); }} onKeyDown={event => { if (slashOpen && slashKeyboardRef.current?.(event)) return; if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} onPaste={event => { if (composerMode === "goal") return; const files = Array.from(event.clipboardData.items).filter(item => item.type.startsWith("image/")).map(item => item.getAsFile()).filter((file): file is File => file !== null); if (files.length) { event.preventDefault(); void addImages(files); } }} placeholder={t(composerMode === "goal" ? "codex.goalComposerPlaceholder" : steering ? "codex.steerPlaceholder" : "codex.messagePlaceholder")} rows={3} /></div>
      <div className="composer-bar"><div>{composerMode === "goal" && <span className="composer-mode-pill"><Target size={15} />{t("codex.goalMode")}<button type="button" title={t("common.close")} aria-label={t("common.close")} onClick={leaveGoalMode}><X size={13} /></button></span>}<CodexModelPicker models={catalogue.models} loading={catalogue.loading} error={catalogue.error} value={model} onChange={setModel} allowServerDefault requestCustom={customModelSignal} /><select aria-label={t("codex.reasoningEffort")} value={reasoningEffort} onChange={event => setReasoningEffort(event.target.value)}><option value="">{t("codex.reasoningDefault")}</option>{codexReasoningOptions.map(option => <option value={option.value} key={option.value}>{option.labelKey ? t(option.labelKey) : option.value}</option>)}</select><select aria-label={t("codex.approveOnRequest")} value={approvalMode} onChange={event => setApprovalMode(event.target.value)}><option value="on-request">{t("codex.approveOnRequest")}</option><option value="untrusted">{t("codex.untrusted")}</option><option value="never">{t("codex.neverApprove")}</option></select></div><button type={showInterrupt ? "button" : "submit"} className={showInterrupt ? "secondary-button danger composer-action" : "primary-button composer-action"} title={t(showInterrupt ? "codex.interrupt" : steering ? "codex.steer" : "codex.send")} aria-label={t(showInterrupt ? "codex.interrupt" : steering ? "codex.steer" : "codex.send")} disabled={sending || interrupting || (!showInterrupt && (slashOpen || !composerHasContent || imageBusy || composerBlocked))} onClick={showInterrupt ? () => void interrupt() : undefined}>{sending || interrupting ? <LoaderCircle className="spin" size={17} /> : showInterrupt ? <Square size={15} /> : <ChevronRight size={17} />}<span className="composer-action-full">{t(showInterrupt ? "codex.interrupt" : steering ? "codex.steer" : "codex.send")}</span><span className="composer-action-compact">{t(showInterrupt ? "codex.interruptShort" : steering ? "codex.steerShort" : "codex.send")}</span></button></div>
    </form></div>}
    <Dialog open={mcpOpen} title={t("codex.mcpTitle")} onClose={() => setMcpOpen(false)}><SnapshotNotice snapshot={mcpSnapshot} loading={nativeBusy === "mcp"} error={nativeError} />{mcpSnapshot?.data?.length ? <div className="native-list">{mcpSnapshot.data.map(server => <article key={server.name}><header><strong>{server.name}</strong><Status value={server.auth_status || "unknown"} /></header>{(server.server_name || server.server_version) && <small>{[server.server_name, server.server_version].filter(Boolean).join(" ")}</small>}<p>{server.tools.length ? server.tools.join(", ") : t("codex.noTools")}</p><small>{t("codex.mcpResources", { resources: server.resource_count, templates: server.resource_template_count })}</small></article>)}</div> : !nativeBusy && <Empty icon={<Network size={24} />} text={t("codex.noMCPServers")} />}<DialogActions><button type="button" className="secondary-button" disabled={nativeBusy === "mcp"} onClick={() => void loadSnapshot<CodexMCPServer[]>("mcp", true)}><RefreshCw size={16} />{t("common.refresh")}</button></DialogActions></Dialog>
    <Dialog open={skillsOpen} title={t("codex.skillsTitle")} onClose={() => setSkillsOpen(false)}><SnapshotNotice snapshot={skillsSnapshot} loading={nativeBusy === "skills"} error={nativeError} />{skillsSnapshot?.data?.length ? <div className="native-list">{skillsSnapshot.data.map(skill => <article key={`${skill.scope}:${skill.name}`}><header><strong>{skill.display_name || skill.name}</strong><Status value={skill.enabled ? "enabled" : "disabled"} /></header><p>{skill.short_description || skill.description}</p><small>{skill.scope}</small></article>)}</div> : !nativeBusy && <Empty icon={<Boxes size={24} />} text={t("codex.noSkills")} />}<DialogActions><button type="button" className="secondary-button" disabled={nativeBusy === "skills"} onClick={() => void loadSnapshot<CodexSkill[]>("skills", true)}><RefreshCw size={16} />{t("common.refresh")}</button></DialogActions></Dialog>
    <Dialog open={subagentsOpen} title={t("codex.subagentsTitle")} onClose={() => setSubagentsOpen(false)} wide>{subagents.length > 0 ? <div className="subagent-list">{subagents.map(agent => <article key={agent.threadID}><header><span><Users size={15} /><strong>{agent.path || t("codex.subagent")}</strong></span><Status value={agent.status} /></header><code>{agent.threadID}</code>{(agent.model || agent.reasoningEffort) && <small>{[agent.model, agent.reasoningEffort].filter(Boolean).join(" · ")}</small>}{agent.prompt && <p>{agent.prompt}</p>}{agent.message && <p className="subagent-message">{agent.message}</p>}<button type="button" className="secondary-button" onClick={() => setSelectedSubagent({ id: agent.threadID, native_id: agent.threadID, name: agent.path, status: agent.status, prompt: agent.prompt, model: agent.model, updated_at: agent.updatedAt, summary: agent.message })}>{t("subagents.viewContent")}</button></article>)}</div> : <Empty icon={<Users size={24} />} text={t("codex.noSubagents")} />}</Dialog>
    {selectedSubagent && <SubagentDetails key={selectedSubagent.id} agent={selectedSubagent} path={`/sessions/codex/${encodeURIComponent(thread.id)}/subagents`} revision={realtime} onClose={() => setSelectedSubagent(null)} />}
    <CollaborationTaskDialog open={collabOpen} engine="codex" sessionID={thread.id} workspaceID={thread.workspace_id} currentModel={model} onClose={() => setCollabOpen(false)} onSubmit={text => { setCollabOpen(false); setComposerMode("message"); setEditingEventID(""); submitPrompt(text); }} />
    <Dialog open={planOpen} title={t("codex.planTitle")} onClose={() => setPlanOpen(false)}><div className="unsupported-state"><StickyNote size={24} /><strong>{t("codex.planUnsupportedTitle")}</strong><p>{t("codex.planUnsupportedReason")}</p></div></Dialog>
  </>;
}

function SnapshotNotice({ snapshot, loading, error }: { snapshot?: CodexSnapshot<unknown> | null; loading: boolean; error: string }) {
  const { t } = useI18n();
  if (loading && !snapshot?.data) return <div className="snapshot-notice"><LoaderCircle className="spin" size={16} />{t("common.loading")}</div>;
  if (error) return <ErrorBanner text={error} />;
  if (snapshot && (!snapshot.supported || snapshot.status === "unsupported")) return <div className="snapshot-notice warning"><AlertTriangle size={16} />{snapshot.reason || t("codex.unsupported")}</div>;
  if (snapshot?.status === "failed") return <div className="snapshot-notice warning"><AlertTriangle size={16} />{snapshot.error || t("codex.snapshotFailed")}</div>;
  if (snapshot?.updated_at) return <p className="snapshot-meta">{loading ? t("codex.refreshing") : t("codex.cachedAt", { time: formatDate(snapshot.updated_at) })}</p>;
  return null;
}
