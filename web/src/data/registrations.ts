import type { SSHBootstrapResult } from "../types";
import { mutation, query, writeResource } from "./resources";

export type ServerRegistration = {
  id: string; name: string; state: string; server_id: string;
  expires_at: string; session_expires_at: string; error_code: string;
  progress: Array<{step: string; current?: number; total?: number}>;
  result: Partial<SSHBootstrapResult>;
};
export type RegistrationCommand = {registration: ServerRegistration; command: string; code: string};
export type RegistrationInput = {name: string; scan_roots: string[]; configuration: string; notes: string; codex_profile_id: string; git_profile_id: string; os: "linux" | "darwin" | "windows"; repair_server_id?: string};
export const registrations = {
  list: () => query<ServerRegistration[]>("/servers/registrations", ["servers"], false, 750),
  create: (input: RegistrationInput) => writeResource(mutation<RegistrationCommand>("/servers/registrations", "POST", ["servers"]), input),
  revoke: (id: string) => writeResource(mutation(`/servers/registrations/${id}`, "DELETE", ["servers"])),
};
