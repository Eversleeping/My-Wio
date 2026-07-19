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
