import type { StreamEvent } from "./types";
import type { Question } from "./components/UserInputPrompt";

export type GeminiMessage = { id: string; role: "user" | "assistant" | "tool" | "error" | "status" | "question"; text: string; time: string; title?: string; toolName?: string; toolInput?: string; toolOutput?: string; toolStatus?: string; stats?: Record<string, unknown>; images?: { data_url: string }[]; operationID?: string; questions?: Question[]; answers?: Record<string, string> };

export type GeminiDisplayItem = GeminiMessage | { id: string; role: "commandGroup"; items: GeminiMessage[] };

// Group the display only: exports and tool/result pairing keep the flat history.
export function groupGeminiMessages(messages: GeminiMessage[]): GeminiDisplayItem[] {
  const result: GeminiDisplayItem[] = [];
  for (const item of messages) {
    const previous = result.at(-1);
    if (item.role !== "tool") result.push(item);
    else if (previous?.role === "commandGroup" && previous.items[0].operationID === item.operationID) previous.items.push(item);
    else result.push({ id: item.id, role: "commandGroup", items: [item] });
  }
  return result;
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
function readable(value: unknown): string { return typeof value === "string" ? value : value == null ? "" : JSON.stringify(value, null, 2); }

// ACP wraps display text in content blocks; legacy JSONL emits strings/objects.
function toolText(value: unknown): string {
  if (Array.isArray(value) && value.every(block => record(block).type === "content" && record(record(block).content).type === "text")) {
    return value.map(block => readable(record(record(block).content).text)).join("\n");
  }
  return readable(value);
}

function count(...values: unknown[]): number | undefined {
  return values.find(value => typeof value === "number" && Number.isSafeInteger(value) && value >= 0) as number | undefined;
}

function usage(value: unknown): Record<string, unknown> {
  const stats = record(value);
  return { input_tokens: count(stats.input_tokens, stats.input), output_tokens: count(stats.output_tokens, stats.output), tool_calls: count(stats.tool_calls) };
}

// JSONL assistant chunks have no message id. Merge only adjacent chunks from
// the same turn; a tool event or a new user prompt starts a new message.
export function geminiMessages(events: StreamEvent[]): GeminiMessage[] {
  const messages: GeminiMessage[] = [];
  const tools = new Map<string, GeminiMessage>();
  let assistant: GeminiMessage | undefined;
  let operation = "";
  for (const event of events) {
    const p = record(event.payload);
    const op = readable(p.operation_id);
    if (op !== operation) { assistant = undefined; operation = op; }
    const base = { id: event.event_id, time: event.occurred_at, operationID: op };
    if (event.kind === "gemini.user.message") { assistant = undefined; messages.push({ ...base, role: "user", text: readable(p.text), images: Array.isArray(p.images) ? p.images.filter((image): image is {data_url:string} => typeof image?.data_url === "string" && /^data:image\/(png|jpeg|webp);base64,/.test(image.data_url)) : [] }); }
    else if (event.kind === "gemini.question") {
      assistant = undefined;
      messages.push({ ...base, role: "question", text: "", operationID: op, questions: Array.isArray(p.questions) ? p.questions as Question[] : [] });
    } else if (event.kind === "gemini.question.answered") {
      const question = messages.find(item => item.id === p.request_id && item.role === "question");
      if (question) question.answers = Object.fromEntries(Object.entries(record(p.answers)).map(([id, value]) => [id, readable((record(value).answers as unknown[])?.[0])]));
    }
    else if (event.kind === "gemini.message" && p.role === "assistant") {
      const text = readable(p.content);
      if (assistant && p.delta === true) assistant.text += text;
      else { assistant = { ...base, role: "assistant", text }; messages.push(assistant); }
    } else if (event.kind === "gemini.tool_use" || event.kind === "gemini.tool_result") {
      assistant = undefined;
      // IDs are scoped to an operation. Never coalesce by display title: two
      // identical commands can be separate, intentional executions.
      const key = p.tool_id ? JSON.stringify([op, p.tool_id]) : "";
      let tool = key ? tools.get(key) : undefined;
      if (!tool) {
        tool = { ...base, role: "tool", text: "", title: readable(p.tool_name || p.tool_id), toolName: typeof p.tool_name === "string" && /^[a-z_]+$/.test(p.tool_name) ? p.tool_name : readable(p.tool_id || p.tool_name) };
        messages.push(tool);
        if (key) tools.set(key, tool);
      }
      if (p.tool_name) tool.title = readable(p.tool_name);
      if (event.kind === "gemini.tool_use") {
        tool.toolInput = toolText(p.parameters);
        tool.toolStatus ??= "running";
      } else {
        tool.toolOutput = toolText(p.output ?? p.error);
        tool.toolStatus = readable(p.status || (p.error ? "failed" : "completed"));
      }
      tool.text = tool.toolOutput ?? tool.toolInput ?? "";
    } else if (event.kind === "gemini.error") {
      assistant = undefined;
      messages.push({ ...base, role: "error", text: readable(p.message) });
    } else if (event.kind === "gemini.result") {
      assistant = undefined;
      if (p.error) messages.push({ ...base, role: "error", text: readable(record(p.error).message || p.error) });
      if (p.stats) messages.push({ ...base, role: "status", text: "", stats: usage(p.stats) });
    } else if (event.kind === "gemini.turn.completed" && (p.status === "failed" || p.status === "interrupted")) {
      assistant = undefined;
      messages.push({ ...base, role: p.status === "failed" ? "error" : "status", text: p.status === "interrupted" ? "interrupted" : readable(p.message) });
    }
  }
  return messages;
}

// Only the last assistant block in a successful completed turn can be branched.
export function geminiForkTargets(events: StreamEvent[]): Set<string> {
 const targets=new Set<string>(); let operation="", last="", adjacent=false;
 for(const event of events){
  const p=record(event.payload), op=readable(p.operation_id);
  if(op!==operation){operation=op;last="";adjacent=false;}
  if(event.kind==="gemini.message"&&p.role==="assistant") {if(!adjacent || p.delta!==true) last=event.event_id;adjacent=true;}
  else if(event.kind==="gemini.result"){if(p.status==="success"&&last)targets.add(last);adjacent=false;}
  else if(event.kind!=="gemini.init"){adjacent=false;}
 }
 return targets;
}
