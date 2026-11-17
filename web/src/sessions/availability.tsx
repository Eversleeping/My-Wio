import { useResourceRevision } from "../data/resources";
import { useI18n } from "../i18n";
import { locationFor } from "../navigation";
import { useData } from "../useData";
import type { Engine, SessionCapabilities } from "./types";

export type CreateDefaults = { model: string; mode: string; effort: string };
export type EngineAvailability = { engine: Engine; available: boolean; reason: string; capabilities: SessionCapabilities; defaults: CreateDefaults };
export type WorkspaceEngines = { workspace_id: string; server_id: string; engines: EngineAvailability[] };
export function useWorkspaceEngines(workspaceID: string) {
  const revision = useResourceRevision(["servers", "codex", "gemini", "claude", "projects"]);
  return useData<WorkspaceEngines>(workspaceID ? `/workspaces/${encodeURIComponent(workspaceID)}/session-engines` : null, revision);
}
export function EngineUnavailable({ reason }: { reason: string }) {
  const { t } = useI18n();
  const settings = reason === "credentials_missing" || reason === "isolation_unavailable";
  return <p className="error-banner" role="alert"><span>{t(`sessions.unavailable.${reason}`)}</span> <a href={locationFor(settings ? "settings" : "servers")}>{t(settings ? "nav.settings" : "nav.servers")}</a></p>;
}
