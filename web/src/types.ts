export interface Session { username: string; csrf_token: string; expires_at: string }
export interface Server { id: string; name: string; hostname: string; status: string; agent_version: string; codex_version: string; codex_ready: number; last_seen_at: string | null; created_at: string }
export interface SSHHostKey { fingerprint: string; key_type: string }
export interface SSHBootstrapResult { server_id: string; hostname: string; architecture: string; warnings: string[] }
export interface SSHBootstrapStreamEvent { type: "progress" | "heartbeat" | "complete" | "error"; step?: string; current?: number; total?: number; code?: string; error?: string; detail?: string; result?: SSHBootstrapResult }
export interface Project { id: string; name: string; remote_url: string; updated_at: string; workspace_count: number }
export interface Workspace { id: string; project_id: string; server_id: string; path: string; branch: string; commit_sha: string; dirty: number; server_name: string; project_name: string }
export interface Thread { id: string; workspace_id: string; codex_thread_id: string; title: string; status: string; path: string; server_id: string; server_name: string; project_name: string; created_at: string; updated_at: string }
export interface StreamEvent { event_id: string; stream_id: string; sequence: number; kind: string; occurred_at: string; payload: unknown }
export interface Approval { id: string; thread_id: string; request_id: string; kind: string; detail: unknown; status: string; title: string; expires_at: string }
export interface SecretSet { id: string; name: string; updated_at: string }
export interface DeploymentTarget { id: string; project_id: string; server_id: string; secret_set_id: string; environment: string; repository: string; git_ref: string; compose_file: string; working_dir: string; build_mode: string; health_checks: string; release_root: string; project_name: string; server_name: string }
export interface Deployment { id: string; target_id: string; operation_id: string; commit_ref: string; resolved_commit: string; status: string; message: string; project_name: string; environment: string; created_at: string; started_at: string | null; finished_at: string | null }
export interface Alert { id: string; server_id: string; kind: string; severity: string; title: string; detail: string; status: string; server_name: string; opened_at: string; resolved_at: string | null; acknowledged_at: string | null }
export interface Metric { server_id: string; bucket_at: string; cpu_percent: number; memory_percent: number; disk_percent: number; load_1: number; net_rx_bytes: number; net_tx_bytes: number }
export interface AuditEntry { id: string; action: string; resource_type: string; resource_id: string; detail: unknown; ip_address: string; occurred_at: string }
export interface Summary { counts: Record<string, number>; deployments: Deployment[]; alerts: Alert[] }
