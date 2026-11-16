import type { Engine } from "../sessions/types";
import type { AuditEntry, CodexCLISettings, CredentialProfile, ScheduledTask, SecretSet } from "../types";
import { mutation, query, writeResource } from "./resources";

export type AuditListPage = {items: AuditEntry[]; has_more: boolean; next: number | null};
export const auditListPageSize = 50;
export type ScheduledTaskInput = Pick<ScheduledTask, "agent_kind" | "thread_id" | "name" | "prompt" | "schedule" | "timezone" | "enabled" | "model" | "reasoning_effort" | "approval_mode" | "thinking_level">;
export type CredentialProfileInput = Omit<CredentialProfile, "updated_at"> & {secret: string};
export const settings = {
  profiles: () => query<CredentialProfile[]>("/credential-profiles", ["settings", "servers"]),
  secrets: () => query<SecretSet[]>("/secret-sets", ["settings", "deployments"]),
  cli: (engine: Engine) => query<CodexCLISettings>(`/settings/${engine}-cli`, ["settings"]),
  checkUpdates: (engine: Engine) => writeResource(mutation<CodexCLISettings>(`/settings/${engine}-cli/check-updates`, "POST", ["settings"]), {}),
  selectVersion: (engine: Engine, version: string) => writeResource(mutation<CodexCLISettings>(`/settings/${engine}-cli/select-version`, "POST", ["settings"]), {version}),
  audit: (offset?: number) => query<AuditListPage>(`/audit?limit=${auditListPageSize}${offset === undefined ? "" : `&offset=${offset}`}`, ["settings"]),
  scheduledTasks: () => query<ScheduledTask[]>("/scheduled-tasks", ["settings"]),
  geminiModels: (workspaceID: string) => query<{models: {id: string; name: string}[]}>(`/workspaces/${workspaceID}/gemini/models`, ["settings"]),
  saveProfile: (input: CredentialProfileInput) => writeResource(mutation("/credential-profiles", "POST", ["settings"]), input),
  createSecret: (name: string, values: Record<string, string>) => writeResource(mutation("/secret-sets", "POST", ["settings"]), {name, values}),
  saveScheduledTask: (id: string, input: ScheduledTaskInput | Pick<ScheduledTask, "enabled">) => writeResource(mutation(id ? `/scheduled-tasks/${id}` : "/scheduled-tasks", id ? "PUT" : "POST", ["settings"]), input),
  removeScheduledTask: (id: string) => writeResource(mutation(`/scheduled-tasks/${id}`, "DELETE", ["settings"])),
  removeProfile: (id: string) => writeResource(mutation(`/credential-profiles/${id}`, "DELETE", ["settings"])),
  removeSecret: (id: string) => writeResource(mutation(`/secret-sets/${id}`, "DELETE", ["settings"])),
};
