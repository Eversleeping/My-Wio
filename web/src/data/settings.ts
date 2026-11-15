import type { CredentialProfile, SecretSet } from "../types";
import { query } from "./resources";
export const settings = {
  profiles: () => query<CredentialProfile[]>("/credential-profiles", ["settings", "servers"]),
  secrets: () => query<SecretSet[]>("/secret-sets", ["settings", "deployments"]),
};
