import type { Project, Server, Workspace } from "../../types";

export type CreateProjectMode = "blank" | "clone" | "discover";
export type BlankProjectRemoteMode = "none" | "existing" | "create";
export type RemoteRepositoryVisibility = "private" | "internal" | "public";
export type ProjectLifecycleState = "provisioning" | "importing" | "failed" | "partial" | "ready" | "syncing" | "pending";

export type ProjectListRecord = Project;

export type WorkspaceListRecord = Workspace;

export type ProjectServerOption = Pick<Server, "id" | "name" | "status">;

export interface CreateProjectFormValue {
  mode: CreateProjectMode;
  name: string;
  serverID: string;
  destination: string;
  remoteURL: string;
  initialBranch: string;
  initializeReadme: boolean;
  remoteMode: BlankProjectRemoteMode;
  remoteProvider: string;
  remoteNamespace: string;
  remoteRepository: string;
  remoteVisibility: RemoteRepositoryVisibility;
}

export type CreateProjectField =
  | "name"
  | "serverID"
  | "remoteURL"
  | "initialBranch"
  | "remoteMode"
  | "remoteProvider"
  | "remoteRepository";

export type CreateProjectValidationErrors = Partial<Record<CreateProjectField, string>>;

export type BlankProjectRemoteRequest =
  | { mode: "none" }
  | { mode: "existing"; url: string }
  | {
    mode: "create";
    provider: string;
    namespace?: string;
    repository: string;
    visibility: RemoteRepositoryVisibility;
  };

export type CreateProjectRequest =
  | {
    mode: "blank";
    name: string;
    server_id: string;
    destination?: string;
    initial_branch: string;
    initialize_readme: boolean;
    remote: BlankProjectRemoteRequest;
  }
  | {
    mode: "clone";
    remote_url: string;
    name?: string;
    server_id: string;
    destination?: string;
  }
  | {
    mode: "discover";
    server_id: string;
  };

export interface CreateProjectValidationLabels {
  nameRequired: string;
  serverRequired: string;
  remoteURLRequired: string;
  initialBranchRequired: string;
  remoteProviderRequired: string;
  remoteRepositoryRequired: string;
  remoteUnavailable: string;
}

export const createProjectInitialValue: CreateProjectFormValue = {
  mode: "blank",
  name: "",
  serverID: "",
  destination: "",
  remoteURL: "",
  initialBranch: "main",
  initializeReadme: false,
  remoteMode: "none",
  remoteProvider: "gitee",
  remoteNamespace: "",
  remoteRepository: "",
  remoteVisibility: "private"
};

export function newCreateProjectFormValue(
  overrides: Partial<CreateProjectFormValue> = {}
): CreateProjectFormValue {
  return { ...createProjectInitialValue, ...overrides };
}

export function deriveProjectLifecycleState(project: ProjectListRecord): ProjectLifecycleState {
  if (project.status === "provisioning") return "provisioning";
  if (project.status === "partial") return "partial";
  if (project.status === "failed") return "failed";
  if (project.import_status === "queued" || project.import_status === "delivered") return "importing";
  if (project.import_status === "failed") return "failed";
  if (project.status === "ready") return "ready";
  if (project.workspace_count > 0) return "ready";
  if (project.import_status === "succeeded") return "syncing";
  return "pending";
}

export function validateCreateProjectForm(
  value: CreateProjectFormValue,
  labels: CreateProjectValidationLabels
): CreateProjectValidationErrors {
  const errors: CreateProjectValidationErrors = {};

  if (!value.serverID.trim()) errors.serverID = labels.serverRequired;

  if (value.mode === "clone" && !value.remoteURL.trim()) {
    errors.remoteURL = labels.remoteURLRequired;
  }

  if (value.mode === "blank") {
    if (!value.name.trim()) errors.name = labels.nameRequired;
    if (!value.initialBranch.trim()) errors.initialBranch = labels.initialBranchRequired;
    if (value.remoteMode === "existing" && !value.remoteURL.trim()) {
      errors.remoteURL = labels.remoteURLRequired;
    }
    if (value.remoteMode === "create") {
      if (!value.remoteProvider.trim()) errors.remoteProvider = labels.remoteProviderRequired;
      if (!value.remoteRepository.trim()) errors.remoteRepository = labels.remoteRepositoryRequired;
    }
  }

  return errors;
}

export function toCreateProjectRequest(value: CreateProjectFormValue): CreateProjectRequest {
  const destination = optional(value.destination);

  if (value.mode === "discover") {
    return { mode: "discover", server_id: value.serverID.trim() };
  }

  if (value.mode === "clone") {
    return {
      mode: "clone",
      remote_url: value.remoteURL.trim(),
      name: optional(value.name),
      server_id: value.serverID.trim(),
      destination
    };
  }


  let remote: BlankProjectRemoteRequest = { mode: "none" };
  if (value.remoteMode === "existing") {
    remote = { mode: "existing", url: value.remoteURL.trim() };
  } else if (value.remoteMode === "create") {
    remote = {
      mode: "create",
      provider: value.remoteProvider.trim(),
      namespace: optional(value.remoteNamespace),
      repository: value.remoteRepository.trim(),
      visibility: value.remoteVisibility
    };
  }

  return {
    mode: "blank",
    name: value.name.trim(),
    server_id: value.serverID.trim(),
    destination,
    initial_branch: value.initialBranch.trim(),
    initialize_readme: value.initializeReadme,
    remote
  };
}

function optional(value: string): string | undefined {
  const normalized = value.trim();
  return normalized || undefined;
}
