import type { Question, QuestionValue } from "./components/UserInputPrompt";
import type { StreamEvent } from "./types";

export type ClaudeToolItem = { id: string; name: string; input?: string; output?: string; failed?: boolean };
export type ClaudeDisplayItem = { id: string; endEventID?: string; operationID?: string; questions?: Question[]; answers?: Record<string, QuestionValue>; cancelled?: boolean; role: "user" | "assistant" | "toolGroup" | "notice" | "question"; text: string; images?: string[]; tools?: ClaudeToolItem[] };

export function claudeDisplayEvents(events: StreamEvent[], denied: string): ClaudeDisplayItem[] {
  const items: ClaudeDisplayItem[] = [];
  const questionTools = new Set(events.filter(event=>event.kind==="claude.tool_use" && (event.payload as {name?:string}).name==="AskUserQuestion").map(event=>(event.payload as {id?:string}).id));
  let toolOperation = "";
  let toolsByID = new Map<string, ClaudeToolItem>();
  for (const event of events) {
    const p = event.payload && typeof event.payload === "object" ? event.payload as Record<string, unknown> : {};
    const text = typeof p.text === "string" ? p.text : "";
    if (event.kind === "claude.question") {
      items.push({ id:event.event_id,role:"question",text:"",operationID:String(p.operation_id ?? ""),questions:Array.isArray(p.questions) ? p.questions as Question[] : [] });
    } else if (event.kind === "claude.question.answered" || event.kind === "claude.question.cancelled") {
      const item=items.find(item=>item.role==="question" && item.id===p.request_id);
      if(item && event.kind === "claude.question.cancelled")item.cancelled=true;
      if(item && p.answers && typeof p.answers==="object")item.answers=Object.fromEntries(Object.entries(p.answers).map(([id,value])=>{ const answers=(value as {answers?:string[]}).answers ?? []; return [id,item.questions?.find(q=>q.id===id)?.multiSelect ? answers : answers[0] ?? ""]; }));
    } else if (event.kind === "claude.tool_use" || event.kind === "claude.tool_result") {
      if (questionTools.has(p.id as string)) continue;
      const operation = typeof p.operation_id === "string" ? p.operation_id : "";
      let group = items.at(-1);
      if (group?.role !== "toolGroup" || toolOperation !== operation) {
        group = { id: event.event_id, role: "toolGroup", text: "", tools: [] };
        items.push(group);
        toolsByID = new Map();
        toolOperation = operation;
      }
      const key = typeof p.id === "string" ? p.id : "";
      let tool = key ? toolsByID.get(key) : undefined;
      if (!tool) {
        tool = { id: event.event_id, name: "" };
        group.tools!.push(tool);
        if (key) toolsByID.set(key, tool);
      }
      if (event.kind === "claude.tool_use") {
        tool.name = typeof p.name === "string" ? p.name : "";
        tool.input = JSON.stringify(p.input ?? {}, null, 2);
      } else {
        tool.output = typeof p.content === "string" ? p.content : JSON.stringify(p.content ?? "", null, 2);
        tool.failed = p.is_error === true;
      }
    } else if (event.kind === "claude.message") {
      if (!text) continue;
      const previous = items.at(-1);
      if (previous?.role === "assistant") { previous.text += text; previous.endEventID = event.event_id; }
      else items.push({ id: event.event_id, endEventID: event.event_id, role: "assistant", text });
    } else if (event.kind === "claude.user.message" || event.kind === "claude.steer.applied") {
      items.push({ id: event.event_id, role: "user", text, images: Array.isArray(p.images) ? p.images.flatMap(image => typeof image?.data_url === "string" ? [image.data_url] : []) : [] });
    } else if (event.kind === "claude.notice") items.push({ id: event.event_id, role: "notice", text });
    else if (event.kind === "claude.result") {
      if (Array.isArray(p.permission_denials) && p.permission_denials.length) items.push({ id: `${event.event_id}-denied`, role: "notice", text: denied });
      if (Array.isArray(p.errors) && p.errors.length) items.push({ id: `${event.event_id}-errors`, role: "notice", text: p.errors.join("\n") });
    } else if (event.kind === "claude.turn.completed" && typeof p.message === "string" && p.message) items.push({ id: event.event_id, role: "notice", text: p.message });
  }
  return items;
}
