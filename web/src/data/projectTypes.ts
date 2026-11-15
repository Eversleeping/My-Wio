export type RemoteRepositoryVisibility = "private" | "internal" | "public";
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

export type WorkspaceGitAction =
  | { type: "branch.create"; name: string; startPoint: string }
  | { type: "branch.rename"; branch: string; name: string }
  | { type: "branch.delete"; branch: string; force: boolean }
  | { type: "checkout"; ref: string; detach: boolean }
  | { type: "remote.add"; name: string; url: string }
  | { type: "remote.update"; remote: string; url: string }
  | { type: "remote.delete"; remote: string }
  | { type: "fetch"; remote: string }
  | { type: "pull"; remote: string; branch: string }
  | { type: "push"; remote: string; ref: string; setUpstream: boolean }
  | { type: "stage"; paths: string[]; all: boolean }
  | { type: "unstage"; paths: string[]; all: boolean }
  | { type: "discard"; paths: string[]; all: boolean }
  | { type: "commit"; message: string };

