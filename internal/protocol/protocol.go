package protocol

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

const (
	ServiceName          = "wio.agent.v1.AgentService"
	MethodConnect        = "/" + ServiceName + "/Connect"
	ControlKindKeepalive = "keepalive"
)

type AgentEnvelope struct {
	MessageID        string          `json:"message_id"`
	ServerID         string          `json:"server_id"`
	Kind             string          `json:"kind"`
	OccurredAtUnixMS int64           `json:"occurred_at_unix_ms"`
	PayloadJSON      json.RawMessage `json:"payload_json,omitempty"`
}

type ControlEnvelope struct {
	OperationID     string          `json:"operation_id"`
	Kind            string          `json:"kind"`
	CreatedAtUnixMS int64           `json:"created_at_unix_ms"`
	PayloadJSON     json.RawMessage `json:"payload_json,omitempty"`
}

type Heartbeat struct {
	Hostname     string   `json:"hostname"`
	AgentVersion string   `json:"agent_version"`
	CodexVersion string   `json:"codex_version"`
	CodexReady   bool     `json:"codex_ready"`
	ScanRoots    []string `json:"scan_roots"`
	ManagedRoots []string `json:"managed_roots,omitempty"`
}

type Metrics struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
	DiskPercent   float64 `json:"disk_percent"`
	Load1         float64 `json:"load_1"`
	NetRxBytes    uint64  `json:"net_rx_bytes"`
	NetTxBytes    uint64  `json:"net_tx_bytes"`
}

type Repository struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	RemoteURL  string `json:"remote_url"`
	Branch     string `json:"branch"`
	CommitSHA  string `json:"commit_sha"`
	Dirty      bool   `json:"dirty"`
	ServerID   string `json:"server_id,omitempty"`
	Discovered string `json:"discovered_at,omitempty"`
}

type Inventory struct {
	Repositories []Repository `json:"repositories"`
}

type WorkspaceFilesCommand struct {
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
}

type WorkspaceFile struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int64  `json:"size,omitempty"`
}

type WorkspaceFilesResult struct {
	Files     []WorkspaceFile `json:"files"`
	Truncated bool            `json:"truncated"`
}

type WorkspaceFilePreviewCommand struct {
	WorkspaceID string `json:"workspace_id"`
	Root        string `json:"root"`
	Path        string `json:"path"`
}

type WorkspaceFilePreviewResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

type WorkspaceChangesCommand struct {
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
}

type WorkspaceChange struct {
	Path     string `json:"path"`
	OldPath  string `json:"old_path,omitempty"`
	Status   string `json:"status"`
	Staged   bool   `json:"staged"`
	Unstaged bool   `json:"unstaged"`
}

type WorkspaceChangesResult struct {
	Changes []WorkspaceChange `json:"changes"`
}

type WorkspaceDiffCommand struct {
	WorkspaceID string `json:"workspace_id"`
	Root        string `json:"root"`
	Path        string `json:"path"`
	OldPath     string `json:"old_path,omitempty"`
}

type WorkspaceDiffResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

type GitProjectCreateCommand struct {
	ProjectID          string `json:"project_id"`
	WorkspaceID        string `json:"workspace_id"`
	Name               string `json:"name"`
	Destination        string `json:"destination,omitempty"`
	InitialBranch      string `json:"initial_branch"`
	InitializeREADME   bool   `json:"initialize_readme"`
	RemoteURL          string `json:"remote_url,omitempty"`
	RequireEmptyRemote bool   `json:"require_empty_remote,omitempty"`
}

type GitProjectCreateResult struct {
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Unborn    bool   `json:"unborn"`
	RemoteURL string `json:"remote_url,omitempty"`
}

type GitProjectDeleteCommand struct {
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Path        string `json:"path"`
}

type GitProjectDeleteResult struct {
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Path        string `json:"path"`
	Removed     bool   `json:"removed"`
}

// ProjectRemoteResult is returned by the control-plane provider adapter. It
// is deliberately free of credentials and is safe to persist in operation
// metadata and audit records.
type ProjectRemoteResult struct {
	Provider   string `json:"provider,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Repository string `json:"repository,omitempty"`
	FetchURL   string `json:"fetch_url"`
	PushURL    string `json:"push_url"`
	WebURL     string `json:"web_url,omitempty"`
}

type GitWorktreeCreateCommand struct {
	SourceWorkspaceID string `json:"source_workspace_id"`
	TargetWorkspaceID string `json:"target_workspace_id"`
	ProjectID         string `json:"project_id"`
	SourcePath        string `json:"source_path"`
	TargetPath        string `json:"target_path"`
	Branch            string `json:"branch"`
	BaseRef           string `json:"base_ref,omitempty"`
	SourceThreadID    string `json:"source_thread_id,omitempty"`
	TargetThreadID    string `json:"target_thread_id,omitempty"`
	CodexThread       string `json:"codex_thread_id,omitempty"`
	Title             string `json:"title,omitempty"`
}

type GitWorktreeCreateResult struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	CommitSHA   string `json:"commit_sha"`
	CodexThread string `json:"codex_thread_id,omitempty"`
}

type GitWorktreeCleanupCommand struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	Branch     string `json:"branch"`
}

type GitWorkspaceInspectCommand struct {
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
	CommitLimit int    `json:"commit_limit,omitempty"`
}

type GitStatus struct {
	Branch    string `json:"branch,omitempty"`
	Detached  bool   `json:"detached"`
	Unborn    bool   `json:"unborn"`
	Head      string `json:"head,omitempty"`
	Upstream  string `json:"upstream,omitempty"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	Staged    int    `json:"staged"`
	Unstaged  int    `json:"unstaged"`
	Untracked int    `json:"untracked"`
	Dirty     bool   `json:"dirty"`
}

type GitBranch struct {
	Name      string `json:"name"`
	FullName  string `json:"full_name"`
	Kind      string `json:"kind"`
	CommitSHA string `json:"commit_sha"`
	Upstream  string `json:"upstream,omitempty"`
	Current   bool   `json:"current"`
}

type GitRemote struct {
	Name      string   `json:"name"`
	FetchURLs []string `json:"fetch_urls"`
	PushURLs  []string `json:"push_urls"`
}

type GitCommit struct {
	SHA         string    `json:"sha"`
	AuthorName  string    `json:"author_name"`
	AuthorEmail string    `json:"author_email"`
	AuthoredAt  time.Time `json:"authored_at"`
	Title       string    `json:"title"`
	Parents     []string  `json:"parents"`
}

type GitWorkspaceInspectResult struct {
	WorkspaceID string            `json:"workspace_id"`
	Status      GitStatus         `json:"status"`
	Changes     []WorkspaceChange `json:"changes"`
	Branches    []GitBranch       `json:"branches"`
	Remotes     []GitRemote       `json:"remotes"`
	Commits     []GitCommit       `json:"commits"`
	HasMore     bool              `json:"has_more"`
}

type GitWorkspaceWriteCommand struct {
	WorkspaceID string   `json:"workspace_id"`
	Path        string   `json:"path"`
	Action      string   `json:"action"`
	Branch      string   `json:"branch,omitempty"`
	NewBranch   string   `json:"new_branch,omitempty"`
	StartPoint  string   `json:"start_point,omitempty"`
	Remote      string   `json:"remote,omitempty"`
	URL         string   `json:"url,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	Force       bool     `json:"force,omitempty"`
	SetUpstream bool     `json:"set_upstream,omitempty"`
	Detach      bool     `json:"detach,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	All         bool     `json:"all,omitempty"`
	Message     string   `json:"message,omitempty"`
}

type GitWorkspaceWriteResult struct {
	WorkspaceID string                    `json:"workspace_id"`
	Action      string                    `json:"action"`
	Snapshot    GitWorkspaceInspectResult `json:"snapshot"`
}

type GitWorkspaceLifecycleCommand struct {
	WorkspaceID       string `json:"workspace_id"`
	TargetWorkspaceID string `json:"target_workspace_id,omitempty"`
	ProjectID         string `json:"project_id"`
	Action            string `json:"action"`
	SourcePath        string `json:"source_path"`
	TargetPath        string `json:"target_path,omitempty"`
	WorkspaceKind     string `json:"workspace_kind,omitempty"`
	Force             bool   `json:"force,omitempty"`
}

type GitWorkspaceLifecycleResult struct {
	WorkspaceID       string `json:"workspace_id"`
	TargetWorkspaceID string `json:"target_workspace_id,omitempty"`
	Action            string `json:"action"`
	SourcePath        string `json:"source_path"`
	TargetPath        string `json:"target_path,omitempty"`
}

type GitWorkspaceCloneCommand struct {
	WorkspaceID  string `json:"workspace_id"`
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	Destination  string `json:"destination"`
	RemoteURL    string `json:"remote_url"`
	Branch       string `json:"branch"`
	ExpectedHead string `json:"expected_head"`
}

type GitWorkspaceCloneResult struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	CommitSHA   string `json:"commit_sha"`
}

type CodexSnapshotCommand struct {
	ScopeType    string `json:"scope_type"`
	ScopeID      string `json:"scope_id"`
	ThreadID     string `json:"thread_id,omitempty"`
	CodexThread  string `json:"codex_thread_id,omitempty"`
	Workspace    string `json:"workspace,omitempty"`
	CodexVersion string `json:"codex_version,omitempty"`
}

type CodexGoalSetCommand struct {
	CodexSnapshotCommand
	Objective   *string `json:"objective"`
	Status      *string `json:"status,omitempty"`
	TokenBudget *int64  `json:"token_budget,omitempty"`
}

type CodexCapabilityResult struct {
	Supported    bool            `json:"supported"`
	Reason       string          `json:"reason,omitempty"`
	CodexVersion string          `json:"codex_version,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
}

type CodexGoal struct {
	ThreadID        string `json:"thread_id"`
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokenBudget     *int64 `json:"token_budget"`
	TokensUsed      int64  `json:"tokens_used"`
	TimeUsedSeconds int64  `json:"time_used_seconds"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type CodexSkill struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Path             string `json:"path"`
	Scope            string `json:"scope"`
	Enabled          bool   `json:"enabled"`
	DisplayName      string `json:"display_name,omitempty"`
	ShortDescription string `json:"short_description,omitempty"`
}

type CodexMCPServer struct {
	Name                  string   `json:"name"`
	AuthStatus            string   `json:"auth_status"`
	ServerName            string   `json:"server_name"`
	ServerVersion         string   `json:"server_version"`
	Tools                 []string `json:"tools"`
	ResourceCount         int      `json:"resource_count"`
	ResourceTemplateCount int      `json:"resource_template_count"`
}

type CodexRateLimit struct {
	Name        string `json:"name"`
	UsedPercent *int64 `json:"used_percent,omitempty"`
	ResetsAt    string `json:"resets_at,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type CodexStatusSnapshot struct {
	RateLimits []CodexRateLimit `json:"rate_limits"`
}

type StreamEvent struct {
	EventID    string          `json:"event_id"`
	StreamID   string          `json:"stream_id"`
	Sequence   int64           `json:"sequence"`
	Kind       string          `json:"kind"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

type StartTurnCommand struct {
	ThreadID        string      `json:"thread_id"`
	CodexThread     string      `json:"codex_thread_id,omitempty"`
	WorkspaceID     string      `json:"workspace_id"`
	Workspace       string      `json:"workspace"`
	Prompt          string      `json:"prompt"`
	Images          []TurnImage `json:"images,omitempty"`
	Model           string      `json:"model,omitempty"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
	ApprovalMode    string      `json:"approval_mode"`
}

type RewriteTurnCommand struct {
	Start              StartTurnCommand `json:"start"`
	NumTurns           uint32           `json:"num_turns"`
	EditEventID        string           `json:"edit_event_id,omitempty"`
	ReplacementEventID string           `json:"replacement_event_id,omitempty"`
	ReplacementPayload json.RawMessage  `json:"replacement_payload,omitempty"`
	CutoffSequence     int64            `json:"cutoff_sequence,omitempty"`
}

type ForkThreadCommand struct {
	SourceThreadID string `json:"source_thread_id"`
	TargetThreadID string `json:"target_thread_id"`
	CodexThread    string `json:"codex_thread_id"`
	WorkspaceID    string `json:"workspace_id"`
	Workspace      string `json:"workspace"`
	Title          string `json:"title"`
}

type ForkThreadResult struct {
	CodexThread string `json:"codex_thread_id"`
}

type TurnImage struct {
	DataURL string `json:"data_url"`
}

type ApprovalDecisionCommand struct {
	ThreadID  string `json:"thread_id"`
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
}

type InterruptTurnCommand struct {
	ThreadID    string `json:"thread_id"`
	CodexThread string `json:"codex_thread_id"`
	TurnID      string `json:"turn_id,omitempty"`
}

type GitImportCommand struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	RemoteURL   string `json:"remote_url"`
	Destination string `json:"destination,omitempty"`
}

type AgentUpdatePackage struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type AgentUpdateCommand struct {
	Version  string                        `json:"version"`
	Packages map[string]AgentUpdatePackage `json:"packages"`
}

type CodexUpdateCommand struct {
	Version string `json:"version"`
}

type ConfigureCredentialsCommand struct {
	CodexAPIURL    string `json:"codex_api_url"`
	CodexAPIKey    string `json:"codex_api_key"`
	CodexModel     string `json:"codex_model"`
	GitEndpoint    string `json:"git_endpoint,omitempty"`
	GitUsername    string `json:"git_username,omitempty"`
	GitToken       string `json:"git_token,omitempty"`
	GitCommitName  string `json:"git_commit_name,omitempty"`
	GitCommitEmail string `json:"git_commit_email,omitempty"`
	RemoveGit      bool   `json:"remove_git,omitempty"`
}

type DeployCommand struct {
	DeploymentID string            `json:"deployment_id"`
	TargetID     string            `json:"target_id"`
	SourceType   string            `json:"source_type"`
	SourcePath   string            `json:"source_path,omitempty"`
	Repository   string            `json:"repository"`
	CommitRef    string            `json:"commit_ref"`
	ComposeFile  string            `json:"compose_file"`
	WorkingDir   string            `json:"working_dir,omitempty"`
	BuildMode    string            `json:"build_mode"`
	ReleaseRoot  string            `json:"release_root"`
	Environment  map[string]string `json:"environment,omitempty"`
	HealthChecks []HealthCheck     `json:"health_checks,omitempty"`
}

type RollbackCommand struct {
	DeploymentID string `json:"deployment_id"`
	TargetID     string `json:"target_id"`
	ReleaseRoot  string `json:"release_root"`
	ComposeFile  string `json:"compose_file"`
	WorkingDir   string `json:"working_dir,omitempty"`
}

// ContainerActionCommand operates on the Compose project from the target's
// current release. The command is sent through the encrypted operation path
// because Compose interpolation may require deployment secrets.
type ContainerActionCommand struct {
	TargetID    string            `json:"target_id"`
	Action      string            `json:"action"`
	ReleaseRoot string            `json:"release_root"`
	ComposeFile string            `json:"compose_file"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

type ContainerActionResult struct {
	TargetID string `json:"target_id"`
	Action   string `json:"action"`
	State    string `json:"state"`
	Message  string `json:"message,omitempty"`
	Content  string `json:"content,omitempty"`
}

// DeploymentStatus is emitted by an Agent as a deployment advances. Content
// holds the command output or a short process note for the current step.
type DeploymentStatus struct {
	DeploymentID   string `json:"deployment_id"`
	Status         string `json:"status"`
	Message        string `json:"message"`
	ResolvedCommit string `json:"resolved_commit,omitempty"`
	Content        string `json:"content,omitempty"`
}

type HealthCheck struct {
	Type    string `json:"type"`
	Address string `json:"address"`
	Timeout int    `json:"timeout_seconds"`
}

type OperationResult struct {
	OperationID string          `json:"operation_id"`
	Status      string          `json:"status"`
	Message     string          `json:"message,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

type jsonCodec struct{}

func (jsonCodec) Name() string                       { return "json" }
func (jsonCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (jsonCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func Codec() encoding.Codec { return jsonCodec{} }

type AgentServiceServer interface {
	Connect(AgentServiceConnectServer) error
}

type AgentServiceConnectServer interface {
	Send(*ControlEnvelope) error
	Recv() (*AgentEnvelope, error)
	grpc.ServerStream
}

type connectServer struct{ grpc.ServerStream }

func (s *connectServer) Send(m *ControlEnvelope) error { return s.ServerStream.SendMsg(m) }
func (s *connectServer) Recv() (*AgentEnvelope, error) {
	m := new(AgentEnvelope)
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func RegisterAgentServiceServer(s grpc.ServiceRegistrar, srv AgentServiceServer) {
	s.RegisterService(&AgentServiceDesc, srv)
}

var AgentServiceDesc = grpc.ServiceDesc{
	ServiceName: ServiceName,
	HandlerType: (*AgentServiceServer)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName: "Connect", ServerStreams: true, ClientStreams: true,
		Handler: func(srv any, stream grpc.ServerStream) error {
			return srv.(AgentServiceServer).Connect(&connectServer{stream})
		},
	}},
}

type AgentServiceClient interface {
	Connect(context.Context, ...grpc.CallOption) (AgentServiceConnectClient, error)
}

type AgentServiceConnectClient interface {
	Send(*AgentEnvelope) error
	Recv() (*ControlEnvelope, error)
	CloseSend() error
	grpc.ClientStream
}

type agentClient struct{ cc grpc.ClientConnInterface }

func NewAgentServiceClient(cc grpc.ClientConnInterface) AgentServiceClient { return &agentClient{cc} }

func (c *agentClient) Connect(ctx context.Context, opts ...grpc.CallOption) (AgentServiceConnectClient, error) {
	stream, err := c.cc.NewStream(ctx, &AgentServiceDesc.Streams[0], MethodConnect, opts...)
	if err != nil {
		return nil, err
	}
	return &connectClient{stream}, nil
}

type connectClient struct{ grpc.ClientStream }

func (c *connectClient) Send(m *AgentEnvelope) error { return c.ClientStream.SendMsg(m) }
func (c *connectClient) Recv() (*ControlEnvelope, error) {
	m := new(ControlEnvelope)
	if err := c.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}
