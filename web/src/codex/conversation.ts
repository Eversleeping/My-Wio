import { type StreamEvent, type CodexGoal, type CodexSkill, type CodexMCPServer, type CodexStatusData } from "../types";
import { extractImageSources } from "../imageContent";
import { pretty } from "../text";

export type ConversationDisplayItem = { type: "event"; event: StreamEvent } | { type: "commandGroup"; events: StreamEvent[] };

type SubagentActivity = { threadID: string; path: string; status: string; message: string; prompt: string; model: string; reasoningEffort: string; updatedAt: string };

export function normalizeGoal(value: unknown): CodexGoal | null {
  const root = asRecord(value);
  const goal = asRecord(root?.goal) ?? (root && typeof root.objective === "string" ? root : null);
  if (!goal) return null;
  return { thread_id: String(goal.thread_id ?? goal.threadId ?? ""), objective: String(goal.objective ?? ""), status: String(goal.status ?? "active"), token_budget: typeof (goal.token_budget ?? goal.tokenBudget) === "number" ? Number(goal.token_budget ?? goal.tokenBudget) : null, tokens_used: Number(goal.tokens_used ?? goal.tokensUsed ?? 0), time_used_seconds: Number(goal.time_used_seconds ?? goal.timeUsedSeconds ?? 0), created_at: Number(goal.created_at ?? goal.createdAt ?? 0), updated_at: Number(goal.updated_at ?? goal.updatedAt ?? 0) };
}

export function normalizeSkills(value: unknown): CodexSkill[] {
  const root = asRecord(value);
  const groups = Array.isArray(value) ? value : root?.data;
  if (!Array.isArray(groups)) return [];
  const skills = Array.isArray(value) ? value : groups.flatMap(groupValue => { const group = asRecord(groupValue); return Array.isArray(group?.skills) ? group.skills : []; });
  return skills.map(skillValue => {
    const skill = asRecord(skillValue) ?? {}; const detail = asRecord(skill.interface);
    return { name: String(skill.name ?? ""), description: String(skill.description ?? ""), path: String(skill.path ?? ""), scope: String(skill.scope ?? ""), enabled: skill.enabled !== false, display_name: String(skill.display_name ?? detail?.displayName ?? ""), short_description: String(skill.short_description ?? detail?.shortDescription ?? "") };
  }).filter(skill => skill.name);
}

export function normalizeMCP(value: unknown): CodexMCPServer[] {
  const servers = Array.isArray(value) ? value : asRecord(value)?.data;
  if (!Array.isArray(servers)) return [];
  return servers.map(serverValue => { const server = asRecord(serverValue) ?? {}; const info = asRecord(server.serverInfo ?? server.server_info); const tools = Array.isArray(server.tools) ? server.tools.map(tool => typeof tool === "string" ? tool : String(asRecord(tool)?.name ?? "")).filter(Boolean) : []; return { name: String(server.name ?? ""), auth_status: String(server.authStatus ?? server.auth_status ?? "unknown"), server_name: String(server.server_name ?? info?.name ?? ""), server_version: String(server.server_version ?? info?.version ?? ""), tools, resource_count: Number(server.resourceCount ?? server.resource_count ?? 0), resource_template_count: Number(server.resourceTemplateCount ?? server.resource_template_count ?? 0) }; }).filter(server => server.name);
}

export function normalizeStatus(value: unknown): CodexStatusData {
  const root = asRecord(value) ?? {};
  if (Array.isArray(root.rate_limits)) return { ...root, rate_limits: root.rate_limits as CodexStatusData["rate_limits"] };
  const limits = asRecord(root.rateLimits) ?? root;
  const rate_limits = Object.entries(limits).flatMap(([key, raw]) => { const limit = asRecord(raw); if (!limit || (!key.toLowerCase().includes("primary") && !key.toLowerCase().includes("secondary"))) return []; return [{ name: String(limit.limitName ?? limit.limit_name ?? key), used_percent: typeof (limit.usedPercent ?? limit.used_percent) === "number" ? Number(limit.usedPercent ?? limit.used_percent) : undefined, resets_at: typeof (limit.resetsAt ?? limit.resets_at) === "string" ? String(limit.resetsAt ?? limit.resets_at) : undefined }]; });
  return { ...root, rate_limits };
}

export function collectSubagentActivity(events: StreamEvent[]): SubagentActivity[] {
  const agents = new Map<string, SubagentActivity>();
  const update = (threadID: string, changes: Partial<SubagentActivity>, occurredAt: string) => {
    if (!threadID) return;
    const previous = agents.get(threadID) ?? { threadID, path: "", status: "pendingInit", message: "", prompt: "", model: "", reasoningEffort: "", updatedAt: occurredAt };
    agents.set(threadID, { ...previous, ...changes, threadID, updatedAt: occurredAt });
  };
  for (const event of events) {
    if (event.kind !== "codex.item.started" && event.kind !== "codex.item.completed") continue;
    const payload = asRecord(event.payload);
    const item = asRecord(payload?.item);
    if (item?.type === "subAgentActivity") {
      const kind = String(item.kind ?? item.status ?? "started").toLowerCase();
      update(String(item.agentThreadId ?? item.newThreadId ?? item.receiverThreadId ?? ""), { path: String(item.agentPath ?? ""), status: kind === "interrupted" ? "interrupted" : kind === "completed" ? "completed" : kind === "failed" ? "errored" : "running" }, event.occurred_at);
      continue;
    }
    if (item?.type === "collabToolCall") {
      const agentStatus = asRecord(item.agentStatus);
      const status = typeof item.agentStatus === "string" ? item.agentStatus : String(agentStatus?.status ?? item.status ?? "running");
      const message = String(agentStatus?.message ?? item.message ?? "");
      const threadIDs = [item.receiverThreadId, item.newThreadId].filter(value => typeof value === "string" && value);
      for (const threadID of new Set(threadIDs as string[])) {
        update(threadID, { status, message, prompt: String(item.prompt ?? ""), model: String(item.model ?? ""), reasoningEffort: String(item.reasoningEffort ?? "") }, event.occurred_at);
      }
      continue;
    }
    if (item?.type !== "collabAgentToolCall") continue;
    const states = asRecord(item.agentsStates) ?? {};
    const receivers = Array.isArray(item.receiverThreadIds) ? item.receiverThreadIds.map(String) : [];
    for (const threadID of receivers) {
      const state = asRecord(states[threadID]);
      update(threadID, {
        status: String(state?.status ?? (item.status === "failed" ? "errored" : item.status === "completed" ? "completed" : "running")),
        message: String(state?.message ?? ""),
        prompt: String(item.prompt ?? ""),
        model: String(item.model ?? ""),
        reasoningEffort: String(item.reasoningEffort ?? "")
      }, event.occurred_at);
    }
    for (const [threadID, rawState] of Object.entries(states)) {
      const state = asRecord(rawState);
      update(threadID, { status: String(state?.status ?? "running"), message: String(state?.message ?? "") }, event.occurred_at);
    }
  }
  return Array.from(agents.values()).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
}

export function conversationEvents(events: StreamEvent[]) {
  const completedTypes = new Set(["agentMessage", "plan", "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabToolCall", "collabAgentToolCall", "subAgentActivity", "contextCompaction", "webSearch"]);
  const result: StreamEvent[] = [];
  const completedItems = new Set<string>();
  const turnErrors = new Set<string>();
  for (const event of events) {
    if (event.kind === "user.message") {
      result.push(event);
      continue;
    }
    const payload = asRecord(event.payload);
    const turn = asRecord(payload?.turn);
    const turnID = payload?.turnId ?? turn?.id;
    const terminalError = event.kind === "codex.turn.failed" || (event.kind === "codex.error" && payload?.willRetry !== true) || (event.kind === "codex.turn.completed" && turn?.status === "failed");
    if (terminalError && typeof turnID === "string" && turnID) {
      const detail = errorText(event.kind === "codex.turn.completed" ? turn : payload);
      const key = JSON.stringify([event.stream_id, turnID, detail]);
      if (turnErrors.has(key)) continue;
      turnErrors.add(key);
    }
    if (event.kind === "codex.item.started") {
      const item = asRecord(payload?.item);
      if (item?.type === "contextCompaction") result.push(event);
      continue;
    }
    if (event.kind === "codex.item.completed") {
      const item = asRecord(payload?.item);
      // Older Agents persisted replayed notifications with fresh event IDs.
      // Only deduplicate identified items, never matching message text alone.
      if (typeof payload?.turnId === "string" && payload.turnId && typeof item?.id === "string" && item.id) {
        const key = JSON.stringify([event.stream_id, payload.threadId, payload.turnId, item.id]);
        if (completedItems.has(key)) continue;
        completedItems.add(key);
      }
      if (item?.type === "userMessage") {
        const images = extractImageSources(item?.content);
        const text = extractText(item?.content);
        if (images.length > 0) {
          for (let index = result.length - 1; index >= 0; index--) {
            if (result[index].kind !== "user.message") continue;
            const messagePayload = asRecord(result[index].payload);
            if (text && String(messagePayload?.text ?? "") !== text) continue;
            result[index] = { ...result[index], payload: { ...messagePayload, images, image_count: images.length } };
            break;
          }
        }
        continue;
      }
      if (completedTypes.has(String(item?.type ?? "")) || extractImageSources(item).length > 0) result.push(event);
      continue;
    }
    if (event.kind === "codex.error") {
      if (payload?.willRetry !== true) result.push(event);
      continue;
    }
    if (event.kind === "codex.turn.completed") {
      const turn = asRecord(payload?.turn);
      if (turn?.status === "failed" || turn?.status === "interrupted") result.push(event);
      continue;
    }
    if (event.kind === "codex.turn.cancelled") {
      result.push(event);
      continue;
    }
    if (event.kind === "codex.turn.failed" || event.kind === "codex.interrupt.failed" || event.kind === "codex.approval.failed" || event.kind === "codex.compact.failed" || event.kind === "codex.steer.failed") result.push(event);
  }
  return result;
}

export function groupCommandEvents(events: StreamEvent[]): ConversationDisplayItem[] {
  const result: ConversationDisplayItem[] = [];
  for (let index = 0; index < events.length;) {
    if (!isCommandEvent(events[index])) {
      result.push({ type: "event", event: events[index] });
      index++;
      continue;
    }
    const commands: StreamEvent[] = [];
    while (index < events.length && isCommandEvent(events[index])) commands.push(events[index++]);
    if (commands.length === 1) result.push({ type: "event", event: commands[0] });
    else result.push({ type: "commandGroup", events: commands });
  }
  return result;
}

function isCommandEvent(event: StreamEvent) {
  if (event.kind !== "codex.item.completed") return false;
  const payload = asRecord(event.payload);
  return asRecord(payload?.item)?.type === "commandExecution";
}

export function asRecord(value: unknown): Record<string, unknown> | null { return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null; }

export function extractText(payload: unknown): string {
  if (typeof payload === "string") return payload;
  if (Array.isArray(payload)) return payload.map(extractText).filter(Boolean).join("\n");
  const value = asRecord(payload);
  if (!value) return "";
  for (const key of ["delta", "text", "message", "diff", "output"]) if (typeof value[key] === "string") return value[key] as string;
  for (const key of ["content", "item", "error"]) { const text = extractText(value[key]); if (text) return text; }
  return "";
}

export function errorText(payload: Record<string, unknown> | null) { return extractText(payload?.error) || extractText(payload); }

export function toolSummary(item: Record<string, unknown> | null, translate: (key: string, values?: Record<string, string | number>) => string) {
  if (!item) return "";
  const type = String(item.type ?? "");
  const primary = type === "commandExecution" ? String(item.command ?? "") : type === "webSearch" ? String(item.query ?? "") : type === "fileChange" && Array.isArray(item.changes) ? String(item.changes.length) : "";
  const status = typeof item.status === "string" ? item.status : "";
  const statusLabel = status ? translate(`status.${status}`) : "";
  const exitCode = type === "commandExecution" && typeof item.exitCode === "number" ? translate("codex.exitCode", { code: item.exitCode }) : "";
  return [primary, statusLabel, exitCode].filter(Boolean).join(" · ");
}

export function toolDetail(item: Record<string, unknown> | null, translate: (key: string, values?: Record<string, string | number>) => string) {
  if (!item) return "";
  const type = String(item.type ?? "");
  if (type === "commandExecution") {
    const output = typeof item.aggregatedOutput === "string" && item.aggregatedOutput.trim() ? item.aggregatedOutput : translate("codex.noCommandOutput");
    const exitCode = typeof item.exitCode === "number" ? translate("codex.exitCode", { code: item.exitCode }) : "";
    return [`$ ${String(item.command ?? "")}`, output, exitCode].filter(Boolean).join("\n\n");
  }
  if (type === "fileChange" && Array.isArray(item.changes)) return item.changes.map(change => { const value = asRecord(change); return value ? `${String(value.kind ?? "updated")} ${String(value.path ?? "")}\n${String(value.diff ?? "")}`.trim() : pretty(change); }).join("\n\n");
  if (type === "webSearch") return String(item.query ?? "");
  return pretty({ arguments: item.arguments, result: item.result, error: item.error });
}

export function readableKind(kind: string) { return kind.replace(/^codex\./, "").replaceAll(".", " / ").replaceAll("/", " / "); }
