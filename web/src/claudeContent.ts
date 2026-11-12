import { safeImageSource } from "./codexContent";
import { extractImageSources, imageContentText } from "./imageContent";
import type { Question, QuestionValue } from "./components/UserInputPrompt";
import type { StreamEvent } from "./types";

export type ClaudeToolItem = { id: string; name: string; operationID?: string; occurredAt?: string; status?: "running" | "completed" | "failed" | "interrupted" | "unknown"; input?: string; output?: string; images?: string[]; failed?: boolean };
export type ClaudeCompaction = { status: "running" | "completed" | "failed" | "interrupted" | "unknown"; trigger?: "auto" | "manual"; preTokens?: number; postTokens?: number };
export type ClaudeDisplayItem = { id: string; occurredAt?: string; endEventID?: string; operationID?: string; compaction?: ClaudeCompaction; questions?: Question[]; answers?: Record<string, QuestionValue>; cancelled?: boolean; role: "user" | "assistant" | "toolGroup" | "notice" | "question" | "compaction"; text: string; images?: string[]; tools?: ClaudeToolItem[] };

export function claudeDisplayEvents(events: StreamEvent[], denied: string): ClaudeDisplayItem[] {
  const items: ClaudeDisplayItem[] = [];
  const questionTools = new Set(events.filter(event=>event.kind==="claude.tool_use" && (event.payload as {name?:string}).name==="AskUserQuestion").map(event=>(event.payload as {id?:string}).id));
  let toolOperation = "";
  let toolsByID = new Map<string, ClaudeToolItem>();
  const pendingTools = new Set<ClaudeToolItem>();
  const pendingCompactions = new Map<string, ClaudeDisplayItem>();
  for (const event of events) {
    const occurredAt = event.occurred_at && Number.isFinite(Date.parse(event.occurred_at)) ? event.occurred_at : undefined;
    const p = event.payload && typeof event.payload === "object" ? event.payload as Record<string, unknown> : {};
    const text = typeof p.text === "string" ? p.text : "";
    if (["claude.compaction.started", "claude.compaction.completed", "claude.compaction.failed"].includes(event.kind)) {
      const operationID = typeof p.operation_id === "string" ? p.operation_id : "";
      let item = pendingCompactions.get(operationID);
      if (!item) {
        item = { id: event.event_id, occurredAt, operationID, role: "compaction", text: "", compaction: { status: "running" } };
        items.push(item);
      }
      if (event.kind === "claude.compaction.started") pendingCompactions.set(operationID, item);
      else {
        const count = (value: unknown) => typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
        item.compaction = { status: event.kind === "claude.compaction.completed" ? "completed" : "failed", trigger: p.trigger === "auto" || p.trigger === "manual" ? p.trigger : undefined, preTokens: count(p.pre_tokens), postTokens: count(p.post_tokens) };
        item.occurredAt = occurredAt ?? item.occurredAt;
        pendingCompactions.delete(operationID);
      }
    } else if (event.kind === "claude.question") {
      items.push({ id:event.event_id,occurredAt,role:"question",text:"",operationID:String(p.operation_id ?? ""),questions:Array.isArray(p.questions) ? p.questions as Question[] : [] });
    } else if (event.kind === "claude.question.answered" || event.kind === "claude.question.cancelled") {
      const item=items.find(item=>item.role==="question" && item.id===p.request_id);
      if(item && event.kind === "claude.question.cancelled")item.cancelled=true;
      if(item && p.answers && typeof p.answers==="object")item.answers=Object.fromEntries(Object.entries(p.answers).map(([id,value])=>{ const answers=(value as {answers?:string[]}).answers ?? []; return [id,item.questions?.find(q=>q.id===id)?.multiSelect ? answers : answers[0] ?? ""]; }));
    } else if (event.kind === "claude.tool_use" || event.kind === "claude.tool_result") {
      if (questionTools.has(p.id as string)) continue;
      const operation = typeof p.operation_id === "string" ? p.operation_id : "";
      let group = items.at(-1);
      if (group?.role !== "toolGroup" || toolOperation !== operation) {
        group = { id: event.event_id, occurredAt, role: "toolGroup", text: "", tools: [] };
        items.push(group);
        toolsByID = new Map();
        toolOperation = operation;
      }
      const key = typeof p.id === "string" ? p.id : "";
      let tool = key ? toolsByID.get(key) : undefined;
      if (!tool) {
        tool = { id: event.event_id, name: "", operationID: operation, occurredAt, status: "running" };
        group.tools!.push(tool);
        pendingTools.add(tool);
        if (key) toolsByID.set(key, tool);
      }
      if (event.kind === "claude.tool_use") {
        tool.name = typeof p.name === "string" ? p.name : "";
        tool.input = JSON.stringify(p.input ?? {}, null, 2);
      } else {
        tool.output = imageContentText(p.content) ?? JSON.stringify(p.content ?? "", null, 2);
        tool.images = extractImageSources(p.content);
        tool.failed = p.is_error === true;
        tool.status = tool.failed ? "failed" : "completed";
        pendingTools.delete(tool);
      }
    } else if (event.kind === "claude.message") {
      if (!text) continue;
      const previous = items.at(-1);
      if (previous?.role === "assistant") { previous.text += text; previous.endEventID = event.event_id; }
      else items.push({ id: event.event_id, occurredAt, endEventID: event.event_id, role: "assistant", text });
    } else if (event.kind === "claude.user.message" || event.kind === "claude.steer.applied") {
      items.push({ id: event.event_id, occurredAt, role: "user", text, images: Array.isArray(p.images) ? p.images.flatMap(image => { const source = safeImageSource(image?.data_url); return source ? [source] : []; }) : [] });
    } else if (event.kind === "claude.notice") items.push({ id: event.event_id, occurredAt, role: "notice", text });
    else if (event.kind === "claude.result") {
      if (Array.isArray(p.permission_denials) && p.permission_denials.length) items.push({ id: `${event.event_id}-denied`, occurredAt, role: "notice", text: denied });
      if (Array.isArray(p.errors) && p.errors.length) items.push({ id: `${event.event_id}-errors`, occurredAt, role: "notice", text: p.errors.join("\n") });
    } else if (event.kind === "claude.turn.completed") {
      const compaction = pendingCompactions.get(String(p.operation_id ?? ""));
      if (compaction) {
        compaction.compaction!.status = p.status === "interrupted" ? "interrupted" : p.status === "failed" ? "failed" : "unknown";
        pendingCompactions.delete(String(p.operation_id ?? ""));
      }
      for (const tool of pendingTools) {
        if (tool.operationID !== p.operation_id) continue;
        tool.status = p.status === "interrupted" ? "interrupted" : p.status === "failed" ? "failed" : "unknown";
        pendingTools.delete(tool);
      }
      if (typeof p.message === "string" && p.message) items.push({ id: event.event_id, occurredAt, role: "notice", text: p.message });
    }
  }
  return items;
}

export function claudeToolPresentation(tool: ClaudeToolItem) {
  let input: unknown;
  try { input = JSON.parse(tool.input ?? "null"); } catch { /* Preserve legacy unstructured input. */ }
  const fields = input && typeof input === "object" && !Array.isArray(input) ? input as Record<string, unknown> : {};
  const string = (key: string) => typeof fields[key] === "string" ? fields[key] as string : "";
  const name = tool.name;
  const kind = name === "Bash" ? "shell" : /^(Read|LS)$/.test(name) ? "read" : /^(Edit|Write|MultiEdit|NotebookEdit)$/.test(name) ? "edit" : /^(Grep|Glob|WebSearch|WebFetch)$/.test(name) ? "search" : "other";
  const detail = kind === "shell" ? string("command") || string("description")
    : kind === "read" || kind === "edit" ? string("file_path") || string("notebook_path") || string("path")
    : kind === "search" ? string("pattern") || string("query") || string("url")
    : string("description") || string("skill");
  const title = kind === "shell" ? detail || name : [name, detail].filter(Boolean).join(": ");
  const parameters = Object.entries(fields).map(([key, value]) => {
    if (kind === "shell" && key === "command" && typeof value === "string") return value;
    if (kind === "shell" && key === "description" && typeof value === "string") return `(${value})`;
    return `${key}: ${typeof value === "string" ? value : JSON.stringify(value, null, 2)}`;
  }).join("\n");
  let output = tool.output;
  if (output !== undefined) {
    // Native tool results may contain text blocks instead of a plain string.
    try {
      const blocks: unknown = JSON.parse(output);
      if (Array.isArray(blocks) && blocks.length && blocks.every(block => block?.type === "text" && typeof block.text === "string")) output = blocks.map(block => block.text).join("\n");
    } catch { /* Plain tool output needs no decoding. */ }
  }
  return { title, kind, input: parameters || tool.input, output };
}
