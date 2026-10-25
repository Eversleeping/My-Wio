import type { StreamEvent } from "./types";

export type GeminiMessage = { id: string; role: "user" | "assistant" | "tool" | "error" | "status"; text: string; time: string; title?: string; stats?: Record<string, unknown> };

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
function readable(value: unknown): string { return typeof value === "string" ? value : value == null ? "" : JSON.stringify(value, null, 2); }

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
  let assistant: GeminiMessage | undefined;
  let operation = "";
  for (const event of events) {
    const p = record(event.payload);
    const op = readable(p.operation_id);
    if (op !== operation) { assistant = undefined; operation = op; }
    const base = { id: event.event_id, time: event.occurred_at };
    if (event.kind === "gemini.user.message") { assistant = undefined; messages.push({ ...base, role: "user", text: readable(p.text) }); }
    else if (event.kind === "gemini.message" && p.role === "assistant") {
      const text = readable(p.content);
      if (assistant && p.delta === true) assistant.text += text;
      else { assistant = { ...base, role: "assistant", text }; messages.push(assistant); }
    } else if (event.kind === "gemini.tool_use" || event.kind === "gemini.tool_result") {
      assistant = undefined;
      messages.push({ ...base, role: "tool", title: readable(p.tool_name || p.tool_id), text: readable(p.parameters ?? p.output ?? p.error ?? p.status) });
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
