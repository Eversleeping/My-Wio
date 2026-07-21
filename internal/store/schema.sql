CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  totp_secret TEXT NOT NULL,
  recovery_hashes TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  csrf_token TEXT NOT NULL,
  expires_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS servers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  hostname TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'offline',
  agent_version TEXT NOT NULL DEFAULT '',
  codex_version TEXT NOT NULL DEFAULT '',
  codex_ready INTEGER NOT NULL DEFAULT 0,
  scan_roots TEXT NOT NULL DEFAULT '[]',
  managed_roots TEXT NOT NULL DEFAULT '[]',
  last_seen_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  revoked_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS server_metadata (
  server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  address TEXT NOT NULL DEFAULT '',
  configuration TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS agent_credentials (
  server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  revoked_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS enrollment_tokens (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  server_name TEXT NOT NULL,
  scan_roots TEXT NOT NULL DEFAULT '[]',
  expires_at TIMESTAMP NOT NULL,
  consumed_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS enrollment_metadata (
  enrollment_id TEXT PRIMARY KEY REFERENCES enrollment_tokens(id) ON DELETE CASCADE,
  address TEXT NOT NULL DEFAULT '',
  configuration TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  remote_url TEXT NOT NULL DEFAULT '',
  normalized_remote TEXT NOT NULL DEFAULT '',
  default_branch TEXT NOT NULL DEFAULT 'main',
  status TEXT NOT NULL DEFAULT 'ready',
  provision_error TEXT NOT NULL DEFAULT '',
  pinned_at TIMESTAMP,
  hidden_at TIMESTAMP,
  archived_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS projects_remote_unique ON projects(normalized_remote) WHERE normalized_remote <> '';

CREATE TABLE IF NOT EXISTS project_remotes (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT 'origin',
  mode TEXT NOT NULL DEFAULT 'existing',
  provider TEXT NOT NULL DEFAULT '',
  namespace TEXT NOT NULL DEFAULT '',
  repository TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'private',
  credential_profile_id TEXT NOT NULL DEFAULT '',
  fetch_url TEXT NOT NULL DEFAULT '',
  push_url TEXT NOT NULL DEFAULT '',
  web_url TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'ready',
  error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, name)
);

CREATE INDEX IF NOT EXISTS project_remotes_project_idx ON project_remotes(project_id, name);

CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  management_mode TEXT NOT NULL DEFAULT 'observed',
  status TEXT NOT NULL DEFAULT 'ready',
  kind TEXT NOT NULL DEFAULT 'primary',
  parent_workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
  branch TEXT NOT NULL DEFAULT '',
  commit_sha TEXT NOT NULL DEFAULT '',
  dirty INTEGER NOT NULL DEFAULT 0,
  last_git_refresh_at TIMESTAMP,
  git_error TEXT NOT NULL DEFAULT '',
  last_scanned_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(server_id, path)
);

CREATE TABLE IF NOT EXISTS workspace_git_snapshots (
  workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  data TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'idle',
  error TEXT NOT NULL DEFAULT '',
  requested_at TIMESTAMP,
  updated_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS codex_threads (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  codex_thread_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT 'New session',
  status TEXT NOT NULL DEFAULT 'idle',
  last_sequence INTEGER NOT NULL DEFAULT 0,
  pinned_at TIMESTAMP,
  archived_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS workspace_file_snapshots (
  workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  files TEXT NOT NULL DEFAULT '[]',
  truncated INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'idle',
  error TEXT NOT NULL DEFAULT '',
  requested_at TIMESTAMP,
  updated_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS workspace_file_previews (
  workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  path TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  size BIGINT NOT NULL DEFAULT 0,
  truncated INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'idle',
  error TEXT NOT NULL DEFAULT '',
  requested_at TIMESTAMP,
  updated_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS workspace_change_snapshots (
  workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  changes TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'idle',
  error TEXT NOT NULL DEFAULT '',
  requested_at TIMESTAMP,
  updated_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS workspace_diff_previews (
  workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  path TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  additions INTEGER NOT NULL DEFAULT 0,
  deletions INTEGER NOT NULL DEFAULT 0,
  is_binary INTEGER NOT NULL DEFAULT 0,
  truncated INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'idle',
  error TEXT NOT NULL DEFAULT '',
  requested_at TIMESTAMP,
  updated_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS codex_snapshots (
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  data TEXT NOT NULL DEFAULT '{}',
  supported INTEGER NOT NULL DEFAULT 1,
  reason TEXT NOT NULL DEFAULT '',
  codex_version TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'idle',
  error TEXT NOT NULL DEFAULT '',
  requested_at TIMESTAMP,
  updated_at TIMESTAMP,
  PRIMARY KEY(scope_type, scope_id, kind)
);

CREATE TABLE IF NOT EXISTS events (
  event_id TEXT PRIMARY KEY,
  stream_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  kind TEXT NOT NULL,
  occurred_at TIMESTAMP NOT NULL,
  payload TEXT NOT NULL DEFAULT '{}',
  UNIQUE(stream_id, sequence)
);

CREATE INDEX IF NOT EXISTS events_stream_idx ON events(stream_id, sequence);
CREATE INDEX IF NOT EXISTS events_time_idx ON events(occurred_at);

CREATE TABLE IF NOT EXISTS approvals (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL REFERENCES codex_threads(id) ON DELETE CASCADE,
  request_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'pending',
  expires_at TIMESTAMP NOT NULL,
  resolved_at TIMESTAMP,
  decision TEXT,
  UNIQUE(thread_id, request_id)
);

CREATE TABLE IF NOT EXISTS agent_operations (
  id TEXT PRIMARY KEY,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
  workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
  kind TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'queued',
  workspace_write INTEGER NOT NULL DEFAULT 0,
  idempotency_key TEXT NOT NULL UNIQUE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  delivered_at TIMESTAMP,
  completed_at TIMESTAMP,
  result TEXT,
  result_data TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS operations_server_idx ON agent_operations(server_id, status, created_at);

CREATE TABLE IF NOT EXISTS control_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS secret_sets (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  ciphertext TEXT NOT NULL,
  key_version INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS credential_profiles (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  username TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  commit_name TEXT NOT NULL DEFAULT '',
  commit_email TEXT NOT NULL DEFAULT '',
  ciphertext TEXT NOT NULL,
  key_version INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(kind, name)
);

CREATE TABLE IF NOT EXISTS server_credential_profiles (
  server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  codex_profile_id TEXT REFERENCES credential_profiles(id) ON DELETE SET NULL,
  git_profile_id TEXT REFERENCES credential_profiles(id) ON DELETE SET NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS server_credential_updates (
  operation_id TEXT PRIMARY KEY REFERENCES agent_operations(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  codex_profile_id TEXT REFERENCES credential_profiles(id) ON DELETE SET NULL,
  git_profile_id TEXT REFERENCES credential_profiles(id) ON DELETE SET NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS deployment_targets (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  secret_set_id TEXT REFERENCES secret_sets(id) ON DELETE SET NULL,
  environment TEXT NOT NULL,
  repository TEXT NOT NULL,
  git_ref TEXT NOT NULL DEFAULT 'main',
  compose_file TEXT NOT NULL DEFAULT 'compose.yaml',
  working_dir TEXT NOT NULL DEFAULT '',
  build_mode TEXT NOT NULL DEFAULT 'build',
  health_checks TEXT NOT NULL DEFAULT '[]',
  release_root TEXT NOT NULL DEFAULT '/var/lib/wio-agent/releases',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, environment)
);

CREATE TABLE IF NOT EXISTS deployments (
  id TEXT PRIMARY KEY,
  target_id TEXT NOT NULL REFERENCES deployment_targets(id) ON DELETE CASCADE,
  operation_id TEXT,
  commit_ref TEXT NOT NULL,
  resolved_commit TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued',
  message TEXT NOT NULL DEFAULT '',
  started_at TIMESTAMP,
  finished_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS deployments_target_idx ON deployments(target_id, created_at);

CREATE TABLE IF NOT EXISTS metric_rollups (
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  bucket_at TIMESTAMP NOT NULL,
  resolution TEXT NOT NULL DEFAULT 'minute',
  cpu_percent REAL NOT NULL,
  memory_percent REAL NOT NULL,
  disk_percent REAL NOT NULL,
  load_1 REAL NOT NULL,
  net_rx_bytes BIGINT NOT NULL,
  net_tx_bytes BIGINT NOT NULL,
  samples INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY(server_id, bucket_at, resolution)
);

CREATE TABLE IF NOT EXISTS alerts (
  id TEXT PRIMARY KEY,
  server_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  severity TEXT NOT NULL,
  title TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  opened_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at TIMESTAMP,
  acknowledged_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS audit_log (
  id TEXT PRIMARY KEY,
  user_id TEXT,
  action TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '{}',
  ip_address TEXT NOT NULL DEFAULT '',
  occurred_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS alerts_status_idx ON alerts(status, opened_at);
CREATE INDEX IF NOT EXISTS audit_time_idx ON audit_log(occurred_at);
