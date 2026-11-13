-- Source: Wio 0.3.12, commit 74697fb45af6ce19209558eebec6ea871bc60e68.
-- Generated: git archive; temporary Go program called that version's store.Open on an empty PostgreSQL 16 schema; a pg_catalog exporter emitted tables, constraints, and indexes.
-- Structure only; no business data.
CREATE TABLE agent_credentials (
  server_id text NOT NULL,
  token_hash text NOT NULL,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  revoked_at timestamp without time zone
);
CREATE TABLE agent_operations (
  id text NOT NULL,
  server_id text NOT NULL,
  project_id text,
  workspace_id text,
  thread_id text,
  kind text NOT NULL,
  payload text NOT NULL DEFAULT '{}'::text,
  status text NOT NULL DEFAULT 'queued'::text,
  workspace_write integer NOT NULL DEFAULT 0,
  idempotency_key text NOT NULL,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  delivered_at timestamp without time zone,
  started_at timestamp without time zone,
  completed_at timestamp without time zone,
  result text,
  result_data text NOT NULL DEFAULT '{}'::text
);
CREATE TABLE alerts (
  id text NOT NULL,
  server_id text,
  kind text NOT NULL,
  severity text NOT NULL,
  title text NOT NULL,
  detail text NOT NULL DEFAULT ''::text,
  status text NOT NULL DEFAULT 'open'::text,
  opened_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at timestamp without time zone,
  acknowledged_at timestamp without time zone
);
CREATE TABLE approvals (
  id text NOT NULL,
  thread_id text NOT NULL,
  request_id text NOT NULL,
  kind text NOT NULL,
  detail text NOT NULL DEFAULT '{}'::text,
  status text NOT NULL DEFAULT 'pending'::text,
  expires_at timestamp without time zone NOT NULL,
  resolved_at timestamp without time zone,
  decision text
);
CREATE TABLE audit_log (
  id text NOT NULL,
  user_id text,
  action text NOT NULL,
  resource_type text NOT NULL,
  resource_id text NOT NULL DEFAULT ''::text,
  detail text NOT NULL DEFAULT '{}'::text,
  ip_address text NOT NULL DEFAULT ''::text,
  occurred_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE claude_scheduled_tasks (
  thinking_level text NOT NULL DEFAULT ''::text,
  id text NOT NULL,
  thread_id text NOT NULL,
  server_id text NOT NULL,
  name text NOT NULL,
  prompt text NOT NULL,
  schedule text NOT NULL,
  timezone text NOT NULL DEFAULT 'UTC'::text,
  enabled integer NOT NULL DEFAULT 1,
  model text NOT NULL DEFAULT ''::text,
  reasoning_effort text NOT NULL DEFAULT ''::text,
  approval_mode text NOT NULL DEFAULT 'default'::text,
  next_run_at timestamp without time zone NOT NULL,
  last_run_at timestamp without time zone,
  last_run_status text NOT NULL DEFAULT ''::text,
  last_run_message text NOT NULL DEFAULT ''::text,
  last_operation_id text NOT NULL DEFAULT ''::text,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE claude_thread_management (
  thread_id text NOT NULL,
  pinned_at timestamp without time zone,
  fork_snapshot_id text NOT NULL DEFAULT ''::text,
  fork_session_id text NOT NULL DEFAULT ''::text
);
CREATE TABLE claude_thread_options (
  thread_id text NOT NULL,
  effort text NOT NULL DEFAULT ''::text
);
CREATE TABLE claude_threads (
  id text NOT NULL,
  workspace_id text NOT NULL,
  session_id text NOT NULL DEFAULT ''::text,
  title text NOT NULL DEFAULT 'Claude'::text,
  model text NOT NULL DEFAULT 'sonnet'::text,
  permission_mode text NOT NULL DEFAULT 'default'::text,
  status text NOT NULL DEFAULT 'idle'::text,
  operation_id text NOT NULL DEFAULT ''::text,
  archived_at timestamp without time zone,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  model_credential_revision text NOT NULL DEFAULT ''::text
);
CREATE TABLE codex_requests (
  thread_id text NOT NULL,
  request_id text NOT NULL,
  action text NOT NULL,
  input_hash text NOT NULL,
  operation_id text NOT NULL
);
CREATE TABLE codex_scheduled_tasks (
  thinking_level text NOT NULL DEFAULT ''::text,
  id text NOT NULL,
  thread_id text NOT NULL,
  server_id text NOT NULL,
  name text NOT NULL,
  prompt text NOT NULL,
  schedule text NOT NULL,
  timezone text NOT NULL DEFAULT 'UTC'::text,
  enabled integer NOT NULL DEFAULT 1,
  model text NOT NULL DEFAULT ''::text,
  reasoning_effort text NOT NULL DEFAULT ''::text,
  approval_mode text NOT NULL DEFAULT 'on-request'::text,
  next_run_at timestamp without time zone NOT NULL,
  last_run_at timestamp without time zone,
  last_run_status text NOT NULL DEFAULT ''::text,
  last_run_message text NOT NULL DEFAULT ''::text,
  last_operation_id text NOT NULL DEFAULT ''::text,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE codex_snapshots (
  scope_type text NOT NULL,
  scope_id text NOT NULL,
  kind text NOT NULL,
  data text NOT NULL DEFAULT '{}'::text,
  supported integer NOT NULL DEFAULT 1,
  reason text NOT NULL DEFAULT ''::text,
  codex_version text NOT NULL DEFAULT ''::text,
  status text NOT NULL DEFAULT 'idle'::text,
  error text NOT NULL DEFAULT ''::text,
  requested_at timestamp without time zone,
  updated_at timestamp without time zone
);
CREATE TABLE codex_threads (
  id text NOT NULL,
  workspace_id text NOT NULL,
  codex_thread_id text NOT NULL DEFAULT ''::text,
  title text NOT NULL DEFAULT 'New session'::text,
  status text NOT NULL DEFAULT 'idle'::text,
  last_sequence integer NOT NULL DEFAULT 0,
  pinned_at timestamp without time zone,
  archived_at timestamp without time zone,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE control_settings (
  key text NOT NULL,
  value text NOT NULL,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE credential_profiles (
  id text NOT NULL,
  kind text NOT NULL,
  name text NOT NULL,
  endpoint text NOT NULL,
  username text NOT NULL DEFAULT ''::text,
  model text NOT NULL DEFAULT ''::text,
  commit_name text NOT NULL DEFAULT ''::text,
  commit_email text NOT NULL DEFAULT ''::text,
  ciphertext text NOT NULL,
  key_version integer NOT NULL DEFAULT 1,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE deployment_config_snapshots (
  deployment_id text NOT NULL,
  target_id text NOT NULL,
  project_id text NOT NULL,
  project_name text NOT NULL DEFAULT ''::text,
  source_type text NOT NULL DEFAULT 'remote'::text,
  environment text NOT NULL DEFAULT ''::text,
  server_id text NOT NULL DEFAULT ''::text,
  server_name text NOT NULL DEFAULT ''::text,
  workspace_id text NOT NULL DEFAULT ''::text,
  workspace_path text NOT NULL DEFAULT ''::text,
  workspace_name text NOT NULL DEFAULT ''::text,
  repository text NOT NULL DEFAULT ''::text,
  git_ref text NOT NULL DEFAULT ''::text,
  resolved_commit text NOT NULL DEFAULT ''::text,
  compose_file text NOT NULL DEFAULT ''::text,
  working_dir text NOT NULL DEFAULT ''::text,
  build_mode text NOT NULL DEFAULT ''::text,
  release_root text NOT NULL DEFAULT ''::text,
  configured_public_url text NOT NULL DEFAULT ''::text,
  detected_public_url text NOT NULL DEFAULT ''::text,
  health_checks text NOT NULL DEFAULT '[]'::text,
  secret_set_id text NOT NULL DEFAULT ''::text,
  secret_set_name text NOT NULL DEFAULT ''::text,
  secret_set_key_version integer NOT NULL DEFAULT 0,
  secret_set_updated_at timestamp without time zone,
  rollback_of_deployment_id text,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE deployment_container_state (
  target_id text NOT NULL,
  operation_id text,
  action text NOT NULL DEFAULT ''::text,
  status text NOT NULL DEFAULT 'unknown'::text,
  message text NOT NULL DEFAULT ''::text,
  content text NOT NULL DEFAULT ''::text,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE deployment_events (
  id text NOT NULL,
  deployment_id text NOT NULL,
  status text NOT NULL,
  message text NOT NULL DEFAULT ''::text,
  content text NOT NULL DEFAULT ''::text,
  occurred_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE deployment_targets (
  id text NOT NULL,
  project_id text NOT NULL,
  server_id text NOT NULL,
  source_type text NOT NULL DEFAULT 'remote'::text,
  workspace_id text,
  secret_set_id text,
  environment text NOT NULL,
  repository text NOT NULL,
  git_ref text NOT NULL DEFAULT 'main'::text,
  compose_file text NOT NULL DEFAULT 'compose.yaml'::text,
  working_dir text NOT NULL DEFAULT ''::text,
  build_mode text NOT NULL DEFAULT 'build'::text,
  health_checks text NOT NULL DEFAULT '[]'::text,
  release_root text NOT NULL DEFAULT '/var/lib/wio-agent/releases'::text,
  public_url text NOT NULL DEFAULT ''::text,
  detected_public_url text NOT NULL DEFAULT ''::text,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE deployments (
  id text NOT NULL,
  target_id text NOT NULL,
  operation_id text,
  commit_ref text NOT NULL,
  resolved_commit text NOT NULL DEFAULT ''::text,
  status text NOT NULL DEFAULT 'queued'::text,
  message text NOT NULL DEFAULT ''::text,
  started_at timestamp without time zone,
  finished_at timestamp without time zone,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE enrollment_metadata (
  enrollment_id text NOT NULL,
  address text NOT NULL DEFAULT ''::text,
  configuration text NOT NULL DEFAULT ''::text,
  notes text NOT NULL DEFAULT ''::text
);
CREATE TABLE enrollment_tokens (
  id text NOT NULL,
  token_hash text NOT NULL,
  server_id text,
  server_name text NOT NULL,
  scan_roots text NOT NULL DEFAULT '[]'::text,
  expires_at timestamp without time zone NOT NULL,
  consumed_at timestamp without time zone
);
CREATE TABLE events (
  event_id text NOT NULL,
  stream_id text NOT NULL,
  sequence integer NOT NULL,
  kind text NOT NULL,
  occurred_at timestamp without time zone NOT NULL,
  payload text NOT NULL DEFAULT '{}'::text
);
CREATE TABLE gemini_goals (
  id text NOT NULL,
  thread_id text NOT NULL,
  objective text NOT NULL,
  images_json text NOT NULL DEFAULT '[]'::text,
  status text NOT NULL,
  operation_id text NOT NULL,
  continuations integer NOT NULL DEFAULT 0,
  max_continuations integer NOT NULL,
  max_seconds integer NOT NULL,
  report text NOT NULL DEFAULT ''::text,
  evidence text NOT NULL DEFAULT ''::text,
  message text NOT NULL DEFAULT ''::text,
  created_at timestamp without time zone NOT NULL,
  updated_at timestamp without time zone NOT NULL
);
CREATE TABLE gemini_scheduled_tasks (
  thinking_level text NOT NULL DEFAULT ''::text,
  id text NOT NULL,
  thread_id text NOT NULL,
  server_id text NOT NULL,
  name text NOT NULL,
  prompt text NOT NULL,
  schedule text NOT NULL,
  timezone text NOT NULL DEFAULT 'UTC'::text,
  enabled integer NOT NULL DEFAULT 1,
  model text NOT NULL DEFAULT ''::text,
  reasoning_effort text NOT NULL DEFAULT ''::text,
  approval_mode text NOT NULL DEFAULT 'default'::text,
  next_run_at timestamp without time zone NOT NULL,
  last_run_at timestamp without time zone,
  last_run_status text NOT NULL DEFAULT ''::text,
  last_run_message text NOT NULL DEFAULT ''::text,
  last_operation_id text NOT NULL DEFAULT ''::text,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE gemini_threads (
  model_credential_revision text NOT NULL DEFAULT ''::text,
  thinking_level text NOT NULL DEFAULT ''::text,
  id text NOT NULL,
  pinned_at timestamp without time zone,
  workspace_id text NOT NULL,
  session_id text NOT NULL DEFAULT ''::text,
  title text NOT NULL DEFAULT 'Gemini'::text,
  model text NOT NULL DEFAULT 'auto'::text,
  approval_mode text NOT NULL DEFAULT 'default'::text,
  status text NOT NULL DEFAULT 'idle'::text,
  operation_id text NOT NULL DEFAULT ''::text,
  archived_at timestamp without time zone,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE metric_rollups (
  server_id text NOT NULL,
  bucket_at timestamp without time zone NOT NULL,
  resolution text NOT NULL DEFAULT 'minute'::text,
  cpu_percent real NOT NULL,
  memory_percent real NOT NULL,
  disk_percent real NOT NULL,
  memory_used_bytes bigint NOT NULL DEFAULT 0,
  memory_total_bytes bigint NOT NULL DEFAULT 0,
  disk_used_bytes bigint NOT NULL DEFAULT 0,
  disk_total_bytes bigint NOT NULL DEFAULT 0,
  load_1 real NOT NULL,
  net_rx_bytes bigint NOT NULL,
  net_tx_bytes bigint NOT NULL,
  samples integer NOT NULL DEFAULT 1
);
CREATE TABLE project_remotes (
  id text NOT NULL,
  project_id text NOT NULL,
  name text NOT NULL DEFAULT 'origin'::text,
  mode text NOT NULL DEFAULT 'existing'::text,
  provider text NOT NULL DEFAULT ''::text,
  namespace text NOT NULL DEFAULT ''::text,
  repository text NOT NULL DEFAULT ''::text,
  visibility text NOT NULL DEFAULT 'private'::text,
  credential_profile_id text NOT NULL DEFAULT ''::text,
  fetch_url text NOT NULL DEFAULT ''::text,
  push_url text NOT NULL DEFAULT ''::text,
  web_url text NOT NULL DEFAULT ''::text,
  status text NOT NULL DEFAULT 'ready'::text,
  error text NOT NULL DEFAULT ''::text,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE project_server_preferences (
  project_id text NOT NULL,
  server_id text NOT NULL,
  name text NOT NULL DEFAULT ''::text,
  pinned_at timestamp without time zone,
  hidden_at timestamp without time zone
);
CREATE TABLE projects (
  id text NOT NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT ''::text,
  remote_url text NOT NULL DEFAULT ''::text,
  normalized_remote text NOT NULL DEFAULT ''::text,
  default_branch text NOT NULL DEFAULT 'main'::text,
  status text NOT NULL DEFAULT 'ready'::text,
  provision_error text NOT NULL DEFAULT ''::text,
  pinned_at timestamp without time zone,
  hidden_at timestamp without time zone,
  archived_at timestamp without time zone,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE secret_set_versions (
  secret_set_id text NOT NULL,
  key_version integer NOT NULL,
  ciphertext text NOT NULL,
  updated_at timestamp without time zone NOT NULL
);
CREATE TABLE secret_sets (
  id text NOT NULL,
  name text NOT NULL,
  ciphertext text NOT NULL,
  key_version integer NOT NULL DEFAULT 1,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE server_claude_cli (
  server_id text NOT NULL,
  version text NOT NULL DEFAULT ''::text,
  ready integer NOT NULL DEFAULT 0
);
CREATE TABLE server_claude_credential_profiles (
  server_id text NOT NULL,
  profile_id text,
  profile_revision text NOT NULL DEFAULT ''::text,
  model text NOT NULL DEFAULT ''::text,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE server_claude_credential_updates (
  operation_id text NOT NULL,
  server_id text NOT NULL,
  profile_id text,
  profile_revision text NOT NULL DEFAULT ''::text,
  model text NOT NULL DEFAULT ''::text
);
CREATE TABLE server_credential_profiles (
  server_id text NOT NULL,
  codex_profile_id text,
  git_profile_id text,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE server_credential_updates (
  operation_id text NOT NULL,
  server_id text NOT NULL,
  codex_profile_id text,
  git_profile_id text,
  git_profile_ids text NOT NULL DEFAULT '[]'::text,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE server_gemini_credential_profiles (
  profile_revision text NOT NULL DEFAULT ''::text,
  server_id text NOT NULL,
  profile_id text,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE server_gemini_credential_updates (
  profile_revision text NOT NULL DEFAULT ''::text,
  operation_id text NOT NULL,
  server_id text NOT NULL,
  profile_id text
);
CREATE TABLE server_git_credential_profiles (
  server_id text NOT NULL,
  profile_id text NOT NULL,
  "position" integer NOT NULL DEFAULT 0
);
CREATE TABLE server_metadata (
  server_id text NOT NULL,
  address text NOT NULL DEFAULT ''::text,
  configuration text NOT NULL DEFAULT ''::text,
  notes text NOT NULL DEFAULT ''::text,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE servers (
  id text NOT NULL,
  name text NOT NULL,
  hostname text NOT NULL DEFAULT ''::text,
  status text NOT NULL DEFAULT 'offline'::text,
  agent_version text NOT NULL DEFAULT ''::text,
  codex_version text NOT NULL DEFAULT ''::text,
  codex_ready integer NOT NULL DEFAULT 0,
  gemini_version text NOT NULL DEFAULT ''::text,
  gemini_ready integer NOT NULL DEFAULT 0,
  scan_roots text NOT NULL DEFAULT '[]'::text,
  managed_roots text NOT NULL DEFAULT '[]'::text,
  is_control_plane integer NOT NULL DEFAULT 0,
  last_seen_at timestamp without time zone,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  revoked_at timestamp without time zone
);
CREATE TABLE session_links (
  target_key text NOT NULL,
  source_key text NOT NULL,
  root_key text NOT NULL,
  depth integer NOT NULL,
  request_id text NOT NULL,
  relation text NOT NULL,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE session_subscriptions (
  target_key text NOT NULL,
  source_key text NOT NULL,
  auto_continue integer NOT NULL DEFAULT 0,
  grant_version integer NOT NULL,
  last_event_id text NOT NULL DEFAULT ''::text,
  last_continuation_id text NOT NULL DEFAULT ''::text,
  continuation_count integer NOT NULL DEFAULT 0,
  unread_event_id text NOT NULL DEFAULT ''::text
);
CREATE TABLE session_tool_grants (
  source_key text NOT NULL,
  grant_json text NOT NULL,
  parent_key text NOT NULL DEFAULT ''::text,
  parent_version integer NOT NULL DEFAULT 0,
  version integer NOT NULL DEFAULT 1,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE session_tool_requests (
  id text NOT NULL,
  source_key text NOT NULL,
  request_key text NOT NULL,
  tool text NOT NULL,
  arguments text NOT NULL,
  execution_json text NOT NULL DEFAULT '{}'::text,
  grant_version integer NOT NULL,
  status text NOT NULL,
  phase text NOT NULL DEFAULT ''::text,
  target_engine text NOT NULL DEFAULT ''::text,
  target_id text NOT NULL DEFAULT ''::text,
  workspace_id text NOT NULL DEFAULT ''::text,
  operation_id text NOT NULL DEFAULT ''::text,
  result text NOT NULL DEFAULT '{}'::text,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE session_tool_wait_lock (
  id integer NOT NULL
);
CREATE TABLE session_tool_waits (
  source_key text NOT NULL,
  target_key text NOT NULL,
  expires_at timestamp without time zone NOT NULL
);
CREATE TABLE sessions (
  id text NOT NULL,
  user_id text NOT NULL,
  token_hash text NOT NULL,
  csrf_token text NOT NULL,
  expires_at timestamp without time zone NOT NULL,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE users (
  id text NOT NULL,
  username text NOT NULL,
  auth_mode text NOT NULL DEFAULT 'totp'::text,
  password_hash text NOT NULL,
  totp_secret text NOT NULL,
  recovery_hashes text NOT NULL,
  totp_last_counter bigint NOT NULL DEFAULT 0,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE workspace_change_snapshots (
  workspace_id text NOT NULL,
  changes text NOT NULL DEFAULT '[]'::text,
  status text NOT NULL DEFAULT 'idle'::text,
  error text NOT NULL DEFAULT ''::text,
  requested_at timestamp without time zone,
  updated_at timestamp without time zone
);
CREATE TABLE workspace_diff_previews (
  workspace_id text NOT NULL,
  path text NOT NULL DEFAULT ''::text,
  content text NOT NULL DEFAULT ''::text,
  additions integer NOT NULL DEFAULT 0,
  deletions integer NOT NULL DEFAULT 0,
  is_binary integer NOT NULL DEFAULT 0,
  truncated integer NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'idle'::text,
  error text NOT NULL DEFAULT ''::text,
  requested_at timestamp without time zone,
  updated_at timestamp without time zone
);
CREATE TABLE workspace_file_previews (
  workspace_id text NOT NULL,
  path text NOT NULL DEFAULT ''::text,
  content text NOT NULL DEFAULT ''::text,
  mime_type text NOT NULL DEFAULT ''::text,
  encoding text NOT NULL DEFAULT ''::text,
  size bigint NOT NULL DEFAULT 0,
  truncated integer NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'idle'::text,
  error text NOT NULL DEFAULT ''::text,
  requested_at timestamp without time zone,
  updated_at timestamp without time zone
);
CREATE TABLE workspace_file_snapshots (
  workspace_id text NOT NULL,
  files text NOT NULL DEFAULT '[]'::text,
  truncated integer NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'idle'::text,
  error text NOT NULL DEFAULT ''::text,
  requested_at timestamp without time zone,
  updated_at timestamp without time zone
);
CREATE TABLE workspace_git_snapshots (
  workspace_id text NOT NULL,
  data text NOT NULL DEFAULT '{}'::text,
  status text NOT NULL DEFAULT 'idle'::text,
  error text NOT NULL DEFAULT ''::text,
  requested_at timestamp without time zone,
  updated_at timestamp without time zone
);
CREATE TABLE workspaces (
  id text NOT NULL,
  project_id text NOT NULL,
  server_id text NOT NULL,
  path text NOT NULL,
  display_name text NOT NULL DEFAULT ''::text,
  management_mode text NOT NULL DEFAULT 'observed'::text,
  status text NOT NULL DEFAULT 'ready'::text,
  kind text NOT NULL DEFAULT 'primary'::text,
  parent_workspace_id text,
  branch text NOT NULL DEFAULT ''::text,
  commit_sha text NOT NULL DEFAULT ''::text,
  dirty integer NOT NULL DEFAULT 0,
  last_git_refresh_at timestamp without time zone,
  git_error text NOT NULL DEFAULT ''::text,
  last_scanned_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);
ALTER TABLE agent_credentials ADD CONSTRAINT agent_credentials_pkey PRIMARY KEY (server_id);
ALTER TABLE agent_credentials ADD CONSTRAINT agent_credentials_token_hash_key UNIQUE (token_hash);
ALTER TABLE agent_operations ADD CONSTRAINT agent_operations_idempotency_key_key UNIQUE (idempotency_key);
ALTER TABLE agent_operations ADD CONSTRAINT agent_operations_pkey PRIMARY KEY (id);
ALTER TABLE alerts ADD CONSTRAINT alerts_pkey PRIMARY KEY (id);
ALTER TABLE approvals ADD CONSTRAINT approvals_pkey PRIMARY KEY (id);
ALTER TABLE approvals ADD CONSTRAINT approvals_thread_id_request_id_key UNIQUE (thread_id, request_id);
ALTER TABLE audit_log ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);
ALTER TABLE claude_scheduled_tasks ADD CONSTRAINT claude_scheduled_tasks_pkey PRIMARY KEY (id);
ALTER TABLE claude_thread_management ADD CONSTRAINT claude_thread_management_pkey PRIMARY KEY (thread_id);
ALTER TABLE claude_thread_options ADD CONSTRAINT claude_thread_options_pkey PRIMARY KEY (thread_id);
ALTER TABLE claude_threads ADD CONSTRAINT claude_threads_pkey PRIMARY KEY (id);
ALTER TABLE codex_requests ADD CONSTRAINT codex_requests_pkey PRIMARY KEY (thread_id, request_id);
ALTER TABLE codex_scheduled_tasks ADD CONSTRAINT codex_scheduled_tasks_pkey PRIMARY KEY (id);
ALTER TABLE codex_snapshots ADD CONSTRAINT codex_snapshots_pkey PRIMARY KEY (scope_type, scope_id, kind);
ALTER TABLE codex_threads ADD CONSTRAINT codex_threads_pkey PRIMARY KEY (id);
ALTER TABLE control_settings ADD CONSTRAINT control_settings_pkey PRIMARY KEY (key);
ALTER TABLE credential_profiles ADD CONSTRAINT credential_profiles_kind_name_key UNIQUE (kind, name);
ALTER TABLE credential_profiles ADD CONSTRAINT credential_profiles_pkey PRIMARY KEY (id);
ALTER TABLE deployment_config_snapshots ADD CONSTRAINT deployment_config_snapshots_pkey PRIMARY KEY (deployment_id);
ALTER TABLE deployment_container_state ADD CONSTRAINT deployment_container_state_pkey PRIMARY KEY (target_id);
ALTER TABLE deployment_events ADD CONSTRAINT deployment_events_pkey PRIMARY KEY (id);
ALTER TABLE deployment_targets ADD CONSTRAINT deployment_targets_pkey PRIMARY KEY (id);
ALTER TABLE deployment_targets ADD CONSTRAINT deployment_targets_project_id_environment_key UNIQUE (project_id, environment);
ALTER TABLE deployments ADD CONSTRAINT deployments_pkey PRIMARY KEY (id);
ALTER TABLE enrollment_metadata ADD CONSTRAINT enrollment_metadata_pkey PRIMARY KEY (enrollment_id);
ALTER TABLE enrollment_tokens ADD CONSTRAINT enrollment_tokens_pkey PRIMARY KEY (id);
ALTER TABLE enrollment_tokens ADD CONSTRAINT enrollment_tokens_token_hash_key UNIQUE (token_hash);
ALTER TABLE events ADD CONSTRAINT events_pkey PRIMARY KEY (event_id);
ALTER TABLE events ADD CONSTRAINT events_stream_id_sequence_key UNIQUE (stream_id, sequence);
ALTER TABLE gemini_goals ADD CONSTRAINT gemini_goals_pkey PRIMARY KEY (id);
ALTER TABLE gemini_scheduled_tasks ADD CONSTRAINT gemini_scheduled_tasks_pkey PRIMARY KEY (id);
ALTER TABLE gemini_threads ADD CONSTRAINT gemini_threads_pkey PRIMARY KEY (id);
ALTER TABLE metric_rollups ADD CONSTRAINT metric_rollups_pkey PRIMARY KEY (server_id, bucket_at, resolution);
ALTER TABLE project_remotes ADD CONSTRAINT project_remotes_pkey PRIMARY KEY (id);
ALTER TABLE project_remotes ADD CONSTRAINT project_remotes_project_id_name_key UNIQUE (project_id, name);
ALTER TABLE project_server_preferences ADD CONSTRAINT project_server_preferences_pkey PRIMARY KEY (project_id, server_id);
ALTER TABLE projects ADD CONSTRAINT projects_pkey PRIMARY KEY (id);
ALTER TABLE secret_set_versions ADD CONSTRAINT secret_set_versions_pkey PRIMARY KEY (secret_set_id, key_version);
ALTER TABLE secret_sets ADD CONSTRAINT secret_sets_name_key UNIQUE (name);
ALTER TABLE secret_sets ADD CONSTRAINT secret_sets_pkey PRIMARY KEY (id);
ALTER TABLE server_claude_cli ADD CONSTRAINT server_claude_cli_pkey PRIMARY KEY (server_id);
ALTER TABLE server_claude_credential_profiles ADD CONSTRAINT server_claude_credential_profiles_pkey PRIMARY KEY (server_id);
ALTER TABLE server_claude_credential_updates ADD CONSTRAINT server_claude_credential_updates_pkey PRIMARY KEY (operation_id);
ALTER TABLE server_credential_profiles ADD CONSTRAINT server_credential_profiles_pkey PRIMARY KEY (server_id);
ALTER TABLE server_credential_updates ADD CONSTRAINT server_credential_updates_pkey PRIMARY KEY (operation_id);
ALTER TABLE server_gemini_credential_profiles ADD CONSTRAINT server_gemini_credential_profiles_pkey PRIMARY KEY (server_id);
ALTER TABLE server_gemini_credential_updates ADD CONSTRAINT server_gemini_credential_updates_pkey PRIMARY KEY (operation_id);
ALTER TABLE server_git_credential_profiles ADD CONSTRAINT server_git_credential_profiles_pkey PRIMARY KEY (server_id, profile_id);
ALTER TABLE server_metadata ADD CONSTRAINT server_metadata_pkey PRIMARY KEY (server_id);
ALTER TABLE servers ADD CONSTRAINT servers_pkey PRIMARY KEY (id);
ALTER TABLE session_links ADD CONSTRAINT session_links_pkey PRIMARY KEY (target_key);
ALTER TABLE session_links ADD CONSTRAINT session_links_request_id_key UNIQUE (request_id);
ALTER TABLE session_subscriptions ADD CONSTRAINT session_subscriptions_pkey PRIMARY KEY (target_key, source_key);
ALTER TABLE session_tool_grants ADD CONSTRAINT session_tool_grants_pkey PRIMARY KEY (source_key);
ALTER TABLE session_tool_requests ADD CONSTRAINT session_tool_requests_pkey PRIMARY KEY (id);
ALTER TABLE session_tool_requests ADD CONSTRAINT session_tool_requests_source_key_request_key_key UNIQUE (source_key, request_key);
ALTER TABLE session_tool_wait_lock ADD CONSTRAINT session_tool_wait_lock_pkey PRIMARY KEY (id);
ALTER TABLE session_tool_waits ADD CONSTRAINT session_tool_waits_pkey PRIMARY KEY (source_key, target_key);
ALTER TABLE sessions ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);
ALTER TABLE sessions ADD CONSTRAINT sessions_token_hash_key UNIQUE (token_hash);
ALTER TABLE users ADD CONSTRAINT users_pkey PRIMARY KEY (id);
ALTER TABLE users ADD CONSTRAINT users_username_key UNIQUE (username);
ALTER TABLE workspace_change_snapshots ADD CONSTRAINT workspace_change_snapshots_pkey PRIMARY KEY (workspace_id);
ALTER TABLE workspace_diff_previews ADD CONSTRAINT workspace_diff_previews_pkey PRIMARY KEY (workspace_id);
ALTER TABLE workspace_file_previews ADD CONSTRAINT workspace_file_previews_pkey PRIMARY KEY (workspace_id);
ALTER TABLE workspace_file_snapshots ADD CONSTRAINT workspace_file_snapshots_pkey PRIMARY KEY (workspace_id);
ALTER TABLE workspace_git_snapshots ADD CONSTRAINT workspace_git_snapshots_pkey PRIMARY KEY (workspace_id);
ALTER TABLE workspaces ADD CONSTRAINT workspaces_pkey PRIMARY KEY (id);
ALTER TABLE workspaces ADD CONSTRAINT workspaces_server_id_path_key UNIQUE (server_id, path);
ALTER TABLE agent_credentials ADD CONSTRAINT agent_credentials_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE agent_operations ADD CONSTRAINT agent_operations_project_id_fkey FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE SET NULL;
ALTER TABLE agent_operations ADD CONSTRAINT agent_operations_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE agent_operations ADD CONSTRAINT agent_operations_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES codex_threads(id) ON DELETE SET NULL;
ALTER TABLE agent_operations ADD CONSTRAINT agent_operations_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL;
ALTER TABLE alerts ADD CONSTRAINT alerts_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE approvals ADD CONSTRAINT approvals_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES codex_threads(id) ON DELETE CASCADE;
ALTER TABLE claude_scheduled_tasks ADD CONSTRAINT claude_scheduled_tasks_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE claude_scheduled_tasks ADD CONSTRAINT claude_scheduled_tasks_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES claude_threads(id) ON DELETE CASCADE;
ALTER TABLE claude_thread_management ADD CONSTRAINT claude_thread_management_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES claude_threads(id) ON DELETE CASCADE;
ALTER TABLE claude_thread_options ADD CONSTRAINT claude_thread_options_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES claude_threads(id) ON DELETE CASCADE;
ALTER TABLE claude_threads ADD CONSTRAINT claude_threads_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
ALTER TABLE codex_requests ADD CONSTRAINT codex_requests_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES codex_threads(id) ON DELETE CASCADE;
ALTER TABLE codex_scheduled_tasks ADD CONSTRAINT codex_scheduled_tasks_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE codex_scheduled_tasks ADD CONSTRAINT codex_scheduled_tasks_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES codex_threads(id) ON DELETE CASCADE;
ALTER TABLE codex_threads ADD CONSTRAINT codex_threads_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
ALTER TABLE deployment_config_snapshots ADD CONSTRAINT deployment_config_snapshots_deployment_id_fkey FOREIGN KEY (deployment_id) REFERENCES deployments(id) ON DELETE CASCADE;
ALTER TABLE deployment_config_snapshots ADD CONSTRAINT deployment_config_snapshots_rollback_of_deployment_id_fkey FOREIGN KEY (rollback_of_deployment_id) REFERENCES deployments(id) ON DELETE SET NULL;
ALTER TABLE deployment_container_state ADD CONSTRAINT deployment_container_state_target_id_fkey FOREIGN KEY (target_id) REFERENCES deployment_targets(id) ON DELETE CASCADE;
ALTER TABLE deployment_events ADD CONSTRAINT deployment_events_deployment_id_fkey FOREIGN KEY (deployment_id) REFERENCES deployments(id) ON DELETE CASCADE;
ALTER TABLE deployment_targets ADD CONSTRAINT deployment_targets_project_id_fkey FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;
ALTER TABLE deployment_targets ADD CONSTRAINT deployment_targets_secret_set_id_fkey FOREIGN KEY (secret_set_id) REFERENCES secret_sets(id) ON DELETE SET NULL;
ALTER TABLE deployment_targets ADD CONSTRAINT deployment_targets_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE deployment_targets ADD CONSTRAINT deployment_targets_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL;
ALTER TABLE deployments ADD CONSTRAINT deployments_target_id_fkey FOREIGN KEY (target_id) REFERENCES deployment_targets(id) ON DELETE CASCADE;
ALTER TABLE enrollment_metadata ADD CONSTRAINT enrollment_metadata_enrollment_id_fkey FOREIGN KEY (enrollment_id) REFERENCES enrollment_tokens(id) ON DELETE CASCADE;
ALTER TABLE enrollment_tokens ADD CONSTRAINT enrollment_tokens_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE gemini_goals ADD CONSTRAINT gemini_goals_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES gemini_threads(id) ON DELETE CASCADE;
ALTER TABLE gemini_scheduled_tasks ADD CONSTRAINT gemini_scheduled_tasks_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE gemini_scheduled_tasks ADD CONSTRAINT gemini_scheduled_tasks_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES gemini_threads(id) ON DELETE CASCADE;
ALTER TABLE gemini_threads ADD CONSTRAINT gemini_threads_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
ALTER TABLE metric_rollups ADD CONSTRAINT metric_rollups_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE project_remotes ADD CONSTRAINT project_remotes_project_id_fkey FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;
ALTER TABLE project_server_preferences ADD CONSTRAINT project_server_preferences_project_id_fkey FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;
ALTER TABLE project_server_preferences ADD CONSTRAINT project_server_preferences_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE secret_set_versions ADD CONSTRAINT secret_set_versions_secret_set_id_fkey FOREIGN KEY (secret_set_id) REFERENCES secret_sets(id) ON DELETE CASCADE;
ALTER TABLE server_claude_cli ADD CONSTRAINT server_claude_cli_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE server_claude_credential_profiles ADD CONSTRAINT server_claude_credential_profiles_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES credential_profiles(id) ON DELETE SET NULL;
ALTER TABLE server_claude_credential_profiles ADD CONSTRAINT server_claude_credential_profiles_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE server_claude_credential_updates ADD CONSTRAINT server_claude_credential_updates_operation_id_fkey FOREIGN KEY (operation_id) REFERENCES agent_operations(id) ON DELETE CASCADE;
ALTER TABLE server_claude_credential_updates ADD CONSTRAINT server_claude_credential_updates_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES credential_profiles(id) ON DELETE SET NULL;
ALTER TABLE server_claude_credential_updates ADD CONSTRAINT server_claude_credential_updates_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE server_credential_profiles ADD CONSTRAINT server_credential_profiles_codex_profile_id_fkey FOREIGN KEY (codex_profile_id) REFERENCES credential_profiles(id) ON DELETE SET NULL;
ALTER TABLE server_credential_profiles ADD CONSTRAINT server_credential_profiles_git_profile_id_fkey FOREIGN KEY (git_profile_id) REFERENCES credential_profiles(id) ON DELETE SET NULL;
ALTER TABLE server_credential_profiles ADD CONSTRAINT server_credential_profiles_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE server_credential_updates ADD CONSTRAINT server_credential_updates_codex_profile_id_fkey FOREIGN KEY (codex_profile_id) REFERENCES credential_profiles(id) ON DELETE SET NULL;
ALTER TABLE server_credential_updates ADD CONSTRAINT server_credential_updates_git_profile_id_fkey FOREIGN KEY (git_profile_id) REFERENCES credential_profiles(id) ON DELETE SET NULL;
ALTER TABLE server_credential_updates ADD CONSTRAINT server_credential_updates_operation_id_fkey FOREIGN KEY (operation_id) REFERENCES agent_operations(id) ON DELETE CASCADE;
ALTER TABLE server_credential_updates ADD CONSTRAINT server_credential_updates_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE server_gemini_credential_profiles ADD CONSTRAINT server_gemini_credential_profiles_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES credential_profiles(id) ON DELETE SET NULL;
ALTER TABLE server_gemini_credential_profiles ADD CONSTRAINT server_gemini_credential_profiles_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE server_gemini_credential_updates ADD CONSTRAINT server_gemini_credential_updates_operation_id_fkey FOREIGN KEY (operation_id) REFERENCES agent_operations(id) ON DELETE CASCADE;
ALTER TABLE server_gemini_credential_updates ADD CONSTRAINT server_gemini_credential_updates_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES credential_profiles(id) ON DELETE SET NULL;
ALTER TABLE server_gemini_credential_updates ADD CONSTRAINT server_gemini_credential_updates_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE server_git_credential_profiles ADD CONSTRAINT server_git_credential_profiles_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES credential_profiles(id) ON DELETE CASCADE;
ALTER TABLE server_git_credential_profiles ADD CONSTRAINT server_git_credential_profiles_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE server_metadata ADD CONSTRAINT server_metadata_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE session_links ADD CONSTRAINT session_links_request_id_fkey FOREIGN KEY (request_id) REFERENCES session_tool_requests(id);
ALTER TABLE sessions ADD CONSTRAINT sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE workspace_change_snapshots ADD CONSTRAINT workspace_change_snapshots_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
ALTER TABLE workspace_diff_previews ADD CONSTRAINT workspace_diff_previews_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
ALTER TABLE workspace_file_previews ADD CONSTRAINT workspace_file_previews_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
ALTER TABLE workspace_file_snapshots ADD CONSTRAINT workspace_file_snapshots_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
ALTER TABLE workspace_git_snapshots ADD CONSTRAINT workspace_git_snapshots_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;
ALTER TABLE workspaces ADD CONSTRAINT workspaces_parent_workspace_id_fkey FOREIGN KEY (parent_workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL;
ALTER TABLE workspaces ADD CONSTRAINT workspaces_project_id_fkey FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;
ALTER TABLE workspaces ADD CONSTRAINT workspaces_server_id_fkey FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE;
CREATE INDEX operations_completed_idx ON agent_operations USING btree (completed_at) WHERE (status = ANY (ARRAY['succeeded'::text, 'failed'::text, 'cancelled'::text]));
CREATE INDEX operations_kind_project_time_idx ON agent_operations USING btree (kind, project_id, created_at, id);
CREATE INDEX operations_project_idx ON agent_operations USING btree (project_id, created_at);
CREATE UNIQUE INDEX operations_project_import_active_unique ON agent_operations USING btree (server_id, project_id) WHERE ((project_id IS NOT NULL) AND (kind = 'git.import'::text) AND (status = ANY (ARRAY['queued'::text, 'delivered'::text])));
CREATE INDEX operations_server_idx ON agent_operations USING btree (server_id, status, created_at);
CREATE INDEX operations_thread_idx ON agent_operations USING btree (thread_id, created_at);
CREATE UNIQUE INDEX operations_workspace_exclusive_write_unique ON agent_operations USING btree (workspace_id) WHERE ((workspace_id IS NOT NULL) AND (workspace_write = 1) AND (kind <> ALL (ARRAY['claude.turn.start'::text, 'gemini.turn.start'::text, 'gemini.turn.rewrite'::text])) AND (status = ANY (ARRAY['queued'::text, 'delivered'::text, 'running'::text])));
CREATE INDEX operations_workspace_idx ON agent_operations USING btree (workspace_id, created_at);
CREATE INDEX alerts_status_idx ON alerts USING btree (status, opened_at);
CREATE INDEX audit_time_idx ON audit_log USING btree (occurred_at);
CREATE INDEX claude_scheduled_tasks_due_idx ON claude_scheduled_tasks USING btree (enabled, next_run_at);
CREATE INDEX claude_scheduled_tasks_thread_idx ON claude_scheduled_tasks USING btree (thread_id);
CREATE INDEX claude_threads_workspace_idx ON claude_threads USING btree (workspace_id, updated_at);
CREATE INDEX codex_scheduled_tasks_due_idx ON codex_scheduled_tasks USING btree (enabled, next_run_at);
CREATE INDEX codex_scheduled_tasks_thread_idx ON codex_scheduled_tasks USING btree (thread_id);
CREATE INDEX deployment_snapshots_target_idx ON deployment_config_snapshots USING btree (target_id, created_at);
CREATE INDEX deployment_container_state_operation_idx ON deployment_container_state USING btree (operation_id);
CREATE INDEX deployment_events_deployment_idx ON deployment_events USING btree (deployment_id, occurred_at);
CREATE INDEX deployments_target_idx ON deployments USING btree (target_id, created_at);
CREATE INDEX events_collaboration_time_idx ON events USING btree (occurred_at, stream_id) WHERE (kind = ANY (ARRAY['session.collaboration.request'::text, 'session.collaboration.updated'::text, 'session.collaboration.child'::text]));
CREATE INDEX events_operation_time_idx ON events USING btree (occurred_at, stream_id) WHERE (kind = ANY (ARRAY['operation.succeeded'::text, 'operation.failed'::text, 'operation.cancelled'::text]));
CREATE INDEX events_stream_idx ON events USING btree (stream_id, sequence);
CREATE INDEX events_stream_kind_sequence_idx ON events USING btree (stream_id, kind, sequence);
CREATE INDEX events_time_idx ON events USING btree (occurred_at);
CREATE UNIQUE INDEX gemini_goals_active_idx ON gemini_goals USING btree (thread_id) WHERE (status = 'active'::text);
CREATE INDEX gemini_goals_thread_idx ON gemini_goals USING btree (thread_id, created_at);
CREATE INDEX gemini_scheduled_tasks_due_idx ON gemini_scheduled_tasks USING btree (enabled, next_run_at);
CREATE INDEX gemini_scheduled_tasks_thread_idx ON gemini_scheduled_tasks USING btree (thread_id);
CREATE INDEX gemini_threads_workspace_idx ON gemini_threads USING btree (workspace_id, updated_at);
CREATE INDEX metrics_bucket_idx ON metric_rollups USING btree (bucket_at);
CREATE INDEX project_remotes_project_idx ON project_remotes USING btree (project_id, name);
CREATE UNIQUE INDEX projects_remote_unique ON projects USING btree (normalized_remote) WHERE (normalized_remote <> ''::text);
CREATE INDEX session_links_root_idx ON session_links USING btree (root_key);
CREATE INDEX session_links_source_idx ON session_links USING btree (source_key);
CREATE INDEX session_subscriptions_source_idx ON session_subscriptions USING btree (source_key);
CREATE INDEX session_tool_requests_pending_idx ON session_tool_requests USING btree (status, created_at);
CREATE INDEX sessions_expiry_idx ON sessions USING btree (expires_at);
