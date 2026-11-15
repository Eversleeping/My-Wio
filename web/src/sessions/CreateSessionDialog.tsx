import { Suspense, useState } from "react";
import { Dialog, Field } from "../components/FormUI";
import { ErrorState, PageLoading } from "../components/PageUI";
import { useI18n } from "../i18n";
import { isEngine } from "../navigation";
import type { Workspace } from "../types";
import { EngineUnavailable, useWorkspaceEngines } from "./availability";
import { engineModule, engines } from "./engineModules";
import type { AISession, Engine } from "./types";

export function rememberedEngine(workspaceID: string): Engine {
  try { const value = localStorage.getItem(`wio_workspace_engine:${workspaceID}`); if (isEngine(value)) return value; } catch { /* Optional preference. */ }
  return "codex";
}
export function CreateSessionDialog({ initialWorkspaceID, workspaces, currentSession, onClose, onCreated }: { initialWorkspaceID: string; workspaces: Workspace[]; currentSession?: AISession | null; onClose: () => void; onCreated: (thread: AISession) => void }) {
  const { t } = useI18n();
  const [workspaceID, setWorkspaceID] = useState(() => workspaces.some(w => w.id === initialWorkspaceID && w.status === "ready") ? initialWorkspaceID : "");
  const [engine, setEngine] = useState<Engine>(() => rememberedEngine(initialWorkspaceID));
  const [busy, setBusy] = useState(false);
  const availability = useWorkspaceEngines(workspaceID);
  const item = availability.data?.engines?.find(item => item.engine === engine);
  const Create = engineModule(engine).Create;
  const options = workspaces.filter(w => w.status === "ready");
  const readyWorkspace = options.some(w => w.id === workspaceID);
  return <Dialog open title={t("sessions.newSession")} dismissDisabled={busy} onClose={() => { if (!busy) onClose(); }}>
    <div>
    <Field label={t("gemini.workspace")}><select aria-label={t("gemini.workspace")} disabled={busy} required value={workspaceID} onChange={event => { setWorkspaceID(event.target.value); setEngine(rememberedEngine(event.target.value)); }}><option value="">{t("gemini.selectWorkspace")}</option>{options.map(w => <option key={w.id} value={w.id}>{w.project_display_name ?? w.project_name} · {w.server_name} · {w.path}</option>)}</select></Field>
    <Field label={t("sessions.engine")}><select aria-label={t("sessions.engine")} disabled={busy || !workspaceID || !availability.data} value={engine} onChange={event => setEngine(event.target.value as Engine)}>{engines.map(module => { const status = availability.data?.engines?.find(item => item.engine === module.id); return <option key={module.id} value={module.id} disabled={!status?.available}>{module.label}{status && !status.available ? ` · ${t(`sessions.unavailable.${status.reason}`)}` : ""}</option>; })}</select></Field>
    {availability.error ? <ErrorState error={availability.error} reload={availability.reload} /> : workspaceID && availability.loading ? <PageLoading /> : item && !item.available ? <EngineUnavailable reason={item.reason} /> : null}
    {workspaceID && readyWorkspace && item?.available && <Suspense fallback={<PageLoading />}><Create key={`${workspaceID}:${engine}`} workspaceID={workspaceID} workspaces={options} history={currentSession ? [currentSession] : []} onBusyChange={setBusy} fixedWorkspace defaults={item.defaults} disabled={!item.available || Boolean(availability.error)} onCreated={thread => {
      try { localStorage.setItem(`wio_workspace_engine:${workspaceID}`, engine); } catch { /* Creation works without storage. */ }
      onCreated(thread);
    }} /></Suspense>}
    </div>
  </Dialog>;
}
