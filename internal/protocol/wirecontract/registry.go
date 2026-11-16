// Package wirecontract registers the payload and result DTOs used on the Agent wire.
// It is separate from protocol to reuse sessiontools.Call without a dependency cycle.
package wirecontract

import (
	"sort"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

// Contract creates fresh payload/result values for contract checks. A nil Result
// means the operation returns no typed data; receipts still use OperationResult.
// Production command decoding remains permissive for forward compatibility.
type Contract struct {
	Payload func() any
	Result  func() any
}

func dto[T any]() any               { return new(T) }
func command[P any]() Contract      { return Contract{Payload: dto[P]} }
func operation[P, R any]() Contract { return Contract{Payload: dto[P], Result: dto[R]} }

var operations = map[string]Contract{
	protocol.OpAgentUpdate:                command[protocol.AgentUpdateCommand](),
	protocol.OpCLIInstall:                 command[protocol.CLIInstallCommand](),
	protocol.OpClaudeCredentialsConfigure: command[protocol.ConfigureClaudeCredentialsCommand](),
	protocol.OpClaudeQuestionAnswer:       command[protocol.ClaudeAnswerCommand](),
	protocol.OpClaudeSkillsList:           operation[protocol.ClaudeSkillsCommand, protocol.ClaudeSkillsResult](),
	protocol.OpClaudeThreadFork:           operation[protocol.ClaudeForkCommand, protocol.ClaudeForkResult](),
	protocol.OpClaudeTurnInterrupt:        operation[protocol.ClaudeInterruptCommand, protocol.GeminiInterruptResult](),
	protocol.OpClaudeTurnStart:            command[protocol.ClaudeTurnCommand](),
	protocol.OpClaudeTurnSteer:            command[protocol.ClaudeSteerCommand](),
	protocol.OpCodexApproval:              command[protocol.ApprovalDecisionCommand](),
	protocol.OpCodexCredentialsConfigure:  command[protocol.ConfigureCredentialsCommand](),
	protocol.OpCodexGoalClear:             operation[protocol.CodexSnapshotCommand, protocol.CodexCapabilityResult](),
	protocol.OpCodexGoalGet:               operation[protocol.CodexSnapshotCommand, protocol.CodexCapabilityResult](),
	protocol.OpCodexGoalSet:               operation[protocol.CodexGoalSetCommand, protocol.CodexCapabilityResult](),
	protocol.OpCodexMCPList:               operation[protocol.CodexSnapshotCommand, protocol.CodexCapabilityResult](),
	protocol.OpCodexModelsList:            operation[protocol.CodexSnapshotCommand, protocol.CodexCapabilityResult](),
	protocol.OpCodexSkillsList:            operation[protocol.CodexSnapshotCommand, protocol.CodexCapabilityResult](),
	protocol.OpCodexStatusSnapshot:        operation[protocol.CodexSnapshotCommand, protocol.CodexCapabilityResult](),
	protocol.OpCodexThreadCompact:         operation[protocol.CodexSnapshotCommand, protocol.CodexCapabilityResult](),
	protocol.OpCodexThreadFork:            operation[protocol.ForkThreadCommand, protocol.ForkThreadResult](),
	protocol.OpCodexTurnInterrupt:         command[protocol.InterruptTurnCommand](),
	protocol.OpCodexTurnRewrite:           command[protocol.RewriteTurnCommand](),
	protocol.OpCodexTurnStart:             command[protocol.StartTurnCommand](),
	protocol.OpCodexTurnSteer:             command[protocol.SteerTurnCommand](),
	protocol.OpCodexUpdate:                command[protocol.CodexUpdateCommand](),
	protocol.OpCredentialsConfigure:       command[protocol.ConfigureCredentialsCommand](),
	protocol.OpDeployContainer:            operation[protocol.ContainerActionCommand, protocol.ContainerActionResult](),
	protocol.OpDeployRollback:             command[protocol.RollbackCommand](),
	protocol.OpDeployStart:                command[protocol.DeployCommand](),
	protocol.OpGeminiCredentialsConfigure: command[protocol.ConfigureGeminiCredentialsCommand](),
	protocol.OpGeminiQuestionAnswer:       command[protocol.GeminiAnswerCommand](),
	protocol.OpGeminiSkillsList:           operation[protocol.GeminiSkillsCommand, protocol.GeminiSkillsResult](),
	protocol.OpGeminiSkillsManage:         operation[protocol.GeminiSkillManageCommand, protocol.GeminiSkillsResult](),
	protocol.OpGeminiThreadFork:           operation[protocol.GeminiForkCommand, protocol.GeminiForkResult](),
	protocol.OpGeminiTurnInterrupt:        operation[protocol.GeminiInterruptCommand, protocol.GeminiInterruptResult](),
	protocol.OpGeminiTurnRewrite:          command[protocol.GeminiTurnCommand](),
	protocol.OpGeminiTurnStart:            command[protocol.GeminiTurnCommand](),
	protocol.OpGeminiTurnSteer:            command[protocol.GeminiSteerCommand](),
	protocol.OpGitCredentialsConfigure:    command[protocol.ConfigureCredentialsCommand](),
	protocol.OpGitImport:                  command[protocol.GitImportCommand](),
	protocol.OpGitProjectCreate:           operation[protocol.GitProjectCreateCommand, protocol.GitProjectCreateResult](),
	protocol.OpGitProjectDelete:           operation[protocol.GitProjectDeleteCommand, protocol.GitProjectDeleteResult](),
	protocol.OpGitWorkspaceClone:          operation[protocol.GitWorkspaceCloneCommand, protocol.GitWorkspaceCloneResult](),
	protocol.OpGitWorkspaceInspect:        operation[protocol.GitWorkspaceInspectCommand, protocol.GitWorkspaceInspectResult](),
	protocol.OpGitWorkspaceLifecycle:      operation[protocol.GitWorkspaceLifecycleCommand, protocol.GitWorkspaceLifecycleResult](),
	protocol.OpGitWorkspaceWrite:          operation[protocol.GitWorkspaceWriteCommand, protocol.GitWorkspaceWriteResult](),
	protocol.OpGitWorktreeCleanup:         command[protocol.GitWorktreeCleanupCommand](),
	protocol.OpGitWorktreeCreate:          operation[protocol.GitWorktreeCreateCommand, protocol.GitWorktreeCreateResult](),
	protocol.OpInventoryScan:              command[struct{}](),
	protocol.OpOperationInterrupt:         command[protocol.OperationInterruptCommand](),
	protocol.OpWorkspaceChanges:           operation[protocol.WorkspaceChangesCommand, protocol.WorkspaceChangesResult](),
	protocol.OpWorkspaceDiffPreview:       operation[protocol.WorkspaceDiffCommand, protocol.WorkspaceDiffResult](),
	protocol.OpWorkspaceFilePreview:       operation[protocol.WorkspaceFilePreviewCommand, protocol.WorkspaceFilePreviewResult](),
	protocol.OpWorkspaceFileWrite:         command[protocol.WorkspaceFileWriteCommand](),
	protocol.OpWorkspaceFiles:             operation[protocol.WorkspaceFilesCommand, protocol.WorkspaceFilesResult](),
	protocol.OpWorkspaceImagePreview:      operation[protocol.WorkspaceFilePreviewCommand, protocol.WorkspaceFilePreviewResult](),
}

var upstream = map[string]Contract{
	protocol.AgentKindAgentUpdateStatus:     command[protocol.AgentUpdateStatus](),
	protocol.AgentKindClaudeEvent:           command[protocol.StreamEvent](),
	protocol.AgentKindClaudeOwnershipActive: command[protocol.GeminiOwnership](),
	protocol.AgentKindClaudeOwnershipLost:   command[protocol.GeminiOwnership](),
	protocol.AgentKindDeploymentStatus:      command[protocol.DeploymentStatus](),
	protocol.AgentKindEvent:                 command[protocol.StreamEvent](),
	protocol.AgentKindGeminiEvent:           command[protocol.StreamEvent](),
	protocol.AgentKindGeminiOwnershipActive: command[protocol.GeminiOwnership](),
	protocol.AgentKindGeminiOwnershipLost:   command[protocol.GeminiOwnership](),
	protocol.AgentKindHeartbeat:             command[protocol.Heartbeat](),
	protocol.AgentKindInventory:             command[protocol.Inventory](),
	protocol.AgentKindMetrics:               command[protocol.Metrics](),
	protocol.AgentKindOperationReconcile:    command[protocol.OperationReconcile](),
	protocol.AgentKindOperationResult:       command[protocol.OperationResult](),
	protocol.AgentKindOperationStarted:      command[protocol.OperationStarted](),
	protocol.AgentKindSessionToolRequest:    command[sessiontools.Call](),
}

// Operation looks up an exact operation kind, excluding prefix families.
func Operation(kind string) (Contract, bool) { c, ok := operations[kind]; return c, ok }

// Upstream looks up an Agent envelope payload.
func Upstream(kind string) (Contract, bool) { c, ok := upstream[kind]; return c, ok }

// OperationKinds returns the sorted exact operation kinds.
func OperationKinds() []string { return kinds(operations) }

// UpstreamKinds returns the sorted Agent envelope kinds.
func UpstreamKinds() []string { return kinds(upstream) }
func kinds(registry map[string]Contract) []string {
	keys := make([]string, 0, len(registry))
	for kind := range registry {
		keys = append(keys, kind)
	}
	sort.Strings(keys)
	return keys
}
