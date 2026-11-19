import { useEffect, useState } from "react";
import { useData } from "../useData";
import { useI18n } from "../i18n";
import type { StreamEvent } from "../types";
import { Dialog } from "./Dialog";
import { Status } from "./PageUI";

type Entry = { id: string; kind: string; text?: string; tool?: string; status?: string; input?: unknown; output?: unknown };
export type NativeSubagent = { id: string; parent_id?: string; native_id?: string; name?: string; status?: string; prompt?: string; model?: string; detail?: string; updated_at: string; summary?: string };
type Page = { items: Array<StreamEvent & { payload: { entry?: Entry } }>; next: number; has_more: boolean };

function outputText(value: unknown) {
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

export function SubagentDetails({ agent, path, revision, onClose }: { agent: NativeSubagent; path: string; revision: unknown; onClose: () => void }) {
  const { t } = useI18n();
  const [events, setEvents] = useState<Page["items"]>([]);
  const [before, setBefore] = useState(0);
  const metadata = useData<{ items: NativeSubagent[] }>(agent.detail ? null : path, revision);
  const current = metadata.data?.items?.find(item => item.id === agent.id) ?? agent;
  const endpoint = `${path}/${encodeURIComponent(agent.id)}/events`;
  const content = useData<Page>(`${endpoint}?after=${before}`, revision);
  const cursor = content.data?.next ?? before;
  const more = content.data?.has_more ?? false;
  useEffect(() => {
    if (!content.data) return;
    setEvents(previous => {
      const bySequence = new Map(previous.map(event => [event.sequence, event]));
      for (const event of content.data!.items) bySequence.set(event.sequence, event);
      return [...bySequence.values()].sort((left, right) => left.sequence - right.sequence);
    });
  }, [content.data]);
  // Follow new events after reaching the current end. A user can page through
  // older output without live updates skipping the unread pages in between.
  useEffect(() => {
    if (!more && cursor > before) setBefore(cursor);
  }, [before, cursor, more]);
  const entries = new Map<string, Entry>();
  for (const event of events) {
    const entry = event.payload.entry;
    if (entry && (entry.kind === "tool" || entry.text || entry.input != null || entry.output != null)) {
      entries.set(entry.id, { ...entries.get(entry.id), ...entry });
    }
  }
  return <Dialog open title={current.name || t("subagents.agent")} onClose={onClose} wide closeLabel={t("common.close")}>
    <div className="subagent-details">
    <div className="subagent-detail-meta"><Status value={current.status || "unknown"} />{current.model && <span>{current.model}</span>}<code>{current.native_id || current.id}</code></div>
    {current.prompt && <p className="subagent-prompt">{current.prompt}</p>}
    {current.detail !== "activity" && entries.size === 0 && <p className="subagent-notice">{t("subagents.resultOnly")}</p>}
    <div className="subagent-transcript" aria-label={t("subagents.content")}>
      {[...entries.values()].map(entry => <article key={entry.id}>
        {entry.kind === "tool" && <header><strong>{entry.tool || t("subagents.tool")}</strong>{entry.status && <Status value={entry.status} />}</header>}
        {entry.text && <div className="subagent-entry-text">{entry.text}</div>}
        {entry.input != null && <details><summary>{t("subagents.input")}</summary><pre>{outputText(entry.input)}</pre></details>}
        {entry.output != null && <details open={entry.status === "failed"}><summary>{t("subagents.output")}</summary><pre>{outputText(entry.output)}</pre></details>}
      </article>)}
      {entries.size === 0 && current.summary && <article className="subagent-entry-text">{current.summary}</article>}
      {entries.size === 0 && !current.summary && !content.loading && !content.error && <p role="status">{t("subagents.noContent")}</p>}
    </div>
    {content.error && <p role="alert">{content.error}<button type="button" className="secondary-button" onClick={content.reload}>{t("common.retry")}</button></p>}
    {content.loading && <p role="status">{t("common.loading")}</p>}
    {more && <button type="button" className="secondary-button" disabled={content.loading} onClick={() => setBefore(cursor)}>{t("subagents.loadMore")}</button>}
    </div>
  </Dialog>;
}
