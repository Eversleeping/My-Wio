package protocol

// The Agent answers a keepalive reconcile request with the stored
// operation_result when it has one, or with an OperationReconcile otherwise.
const (
	OperationReconcileActive  = "active"
	OperationReconcileUnknown = "unknown"
	OperationLostMessage      = "Agent lost track of this operation; it may have restarted before reporting a result"
)

type OperationReconcile struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
}

// SafeDuringAgentRestart reports operation kinds that may be cut short by an
// Agent self-update: read-only scans whose result is simply recomputed, and
// control messages addressed to in-memory sessions that cannot survive a
// restart anyway. Everything else (deployments, Git and file writes, CLI
// installs, credential updates, AI turns) must finish before the Agent execs.
func SafeDuringAgentRestart(kind string) bool {
	for _, safe := range AgentRestartSafeKinds {
		if kind == safe {
			return true
		}
	}
	return false
}

// AgentRestartSafeKinds is shared by the Agent and the control plane so both
// sides refuse an update for the same set of active operations.
var AgentRestartSafeKinds = []string{
	OpAgentAccessRead,
	OpAgentIsolationPlan,
	OpInventoryScan,
	OpWorkspaceFiles,
	OpWorkspaceFilePreview,
	OpWorkspaceImagePreview,
	OpWorkspaceChanges,
	OpWorkspaceDiffPreview,
	OpGitWorkspaceInspect,
	OpClaudeSkillsList,
	OpGeminiSkillsList,
	OpCodexMCPList,
	OpCodexSkillsList,
	OpCodexModelsList,
	OpCodexStatusSnapshot,
	OpOperationInterrupt,
	OpCodexTurnInterrupt,
	OpGeminiTurnInterrupt,
	OpClaudeTurnInterrupt,
	OpCodexTurnSteer,
	OpGeminiTurnSteer,
	OpClaudeTurnSteer,
	OpCodexApproval,
	OpClaudeQuestionAnswer,
	OpGeminiQuestionAnswer,
}
