import type { Server, SSHBootstrapStreamEvent, SSHHostKey } from "../types";
import { api, postStream, post, put } from "./api";
import { invalidateResources, mutation, query, readResource, writeResource } from "./resources";

export type SSHAccess = {host: string; port: number; user: string; auth_method: string; password: string; private_key: string; private_key_passphrase: string; host_key_fingerprint: string};
type Bootstrap = SSHAccess & {name: string; scan_roots: string[]; configuration: string; notes: string; codex_profile_id: string; git_profile_id: string; allow_sudo?: boolean; session_isolation?: boolean};
export type ServerAccessSettings = { allow_sudo: boolean; session_isolation: boolean; revision: string };
export type AgentAccessSettings = ServerAccessSettings & {applied: boolean; restart_required: boolean};
export type AgentAccessProgress = {status:string;settings?:AgentAccessSettings;error?:string};
export const servers = {
 runtimeRead: (id:string) => post<{operation_id:string}>(`/servers/${id}/access-settings/read`,{}),
 runtimeApply: (id:string,settings:AgentAccessSettings) => put<{operation_id:string}>(`/servers/${id}/access-settings`,settings),
 runtimeResult: (id:string,operation:string) => api<AgentAccessProgress>(`/servers/${id}/access-settings/operations/${operation}`),
  access: (id: string, input: SSHAccess & {settings?: ServerAccessSettings}) => writeResource(mutation<ServerAccessSettings>(`/servers/${id}/ssh/access-settings`, "POST", ["servers"]), input),
  list: () => query<Server[]>("/servers", ["servers", "monitoring", "projects", "deployments", "settings"]),
  read: () => readResource(servers.list()),
  probe: (input: {host: string; port: number}) => writeResource(mutation<SSHHostKey>("/servers/ssh/probe", "POST", []), input),
  install: async (repairID: string | undefined, input: Bootstrap, onEvent: (event: SSHBootstrapStreamEvent) => void) => {
    let complete = false, failed = false;
    await postStream<SSHBootstrapStreamEvent>(repairID ? `/servers/${repairID}/ssh/repair-stream` : "/servers/ssh/bootstrap-stream", input, event => {
      if (event.type === "complete" && event.result) complete = true;
      if (event.type === "error") failed = true;
      onEvent(event);
    });
    if (complete && !failed) invalidateResources(["servers"]);
  },
  uninstall: (id: string, input: SSHAccess & {confirmation: string}) => writeResource(mutation(`/servers/${id}/ssh/uninstall`, "POST", ["servers"]), input),
  revoke: (id: string) => writeResource(mutation(`/servers/${id}`, "DELETE", ["servers"])),
  update: (id: string, input: Pick<Server, "address" | "configuration" | "notes">) => writeResource(mutation(`/servers/${id}`, "PATCH", ["servers"]), input),
  updateAgent: (id: string) => writeResource(mutation(`/servers/${id}/agent-update`, "POST", []), {}),
};
