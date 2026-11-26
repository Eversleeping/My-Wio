// Package capabilities resolves Agent declarations after enforcing the minimum
// Agent version. Engine readiness, credentials and native probes remain separate
// from protocol support.
package capabilities

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

// Declaration preserves absent (nil) and explicitly empty sets for storage.
// Neither grants protocol features after the legacy inference is retired.
type Declaration []string

func (d *Declaration) Scan(value any) error {
	*d = nil
	var raw []byte
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("invalid capability declaration type %T", value)
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, d)
}

const MinimumGeminiCLI = "0.66.0"
const MinimumClaudeInteractiveCLI = "2.1.286"

type gate struct {
	name string
	cli  func(gemini, claude string) bool
}

func geminiInteractive(gemini, _ string) bool {
	return buildinfo.VersionAtLeast(gemini, MinimumGeminiCLI)
}
func claudeInteractive(_, claude string) bool {
	return buildinfo.VersionAtLeast(strings.TrimSpace(strings.SplitN(claude, " ", 2)[0]), MinimumClaudeInteractiveCLI)
}

var gates = []gate{
	{protocol.CapabilityAgentAccess, nil},
	{protocol.CapabilityAgentRestart, nil},
	{protocol.CapabilityAgentSelfUpdate, nil},
	{protocol.CapabilityCodexCLIUpdate, nil},
	{protocol.CapabilityDeployRollbackExact, nil},
	{protocol.CapabilityCodexSteer, nil},
	{protocol.CapabilityWorkspaceFileWrite, nil},
	{protocol.CapabilityTaskArtifacts, nil},
	{protocol.CapabilityTaskArtifactVerify, nil},
	{protocol.CapabilityTaskCollaboration, nil},
	{protocol.CapabilityCodexForkAtMessage, nil},
	{protocol.CapabilityGeminiSessions, nil},
	{protocol.CapabilityCLIManage, nil},
	{protocol.CapabilityCLIInstallParallel, nil},
	{protocol.CapabilityGeminiCredentials, nil},
	{protocol.CapabilityGeminiFork, nil},
	{protocol.CapabilityGeminiThinking, nil},
	{protocol.CapabilityGeminiYolo, nil},
	{protocol.CapabilityGeminiInput, nil},
	{protocol.CapabilityGeminiSteer, geminiInteractive},
	{protocol.CapabilityGeminiSkills, geminiInteractive},
	{protocol.CapabilityGeminiPlan, geminiInteractive},
	{protocol.CapabilityGeminiGoal, geminiInteractive},
	{protocol.CapabilityGeminiRewrite, nil},
	{protocol.CapabilityGeminiSteerImages, geminiInteractive},
	{protocol.CapabilityGeminiSkillsManage, geminiInteractive},
	{protocol.CapabilityClaudeSessions, nil},
	{protocol.CapabilityClaudeManage, nil},
	{protocol.CapabilityClaudeEffort, nil},
	{protocol.CapabilityClaudeInput, nil},
	{protocol.CapabilityClaudeSkills, nil},
	{protocol.CapabilityClaudeSteer, claudeInteractive},
	{protocol.CapabilityOperationInterrupt, nil},
	{protocol.CapabilityClaudeRewrite, nil},
	{protocol.CapabilityClaudeQuestions, claudeInteractive},
	{protocol.CapabilityCredentialsScoped, nil},
	{protocol.CapabilitySessionCollaboration, nil},
}

// Supports requires a supported Agent and its explicit declaration. Unknown
// names grant nothing. Self-update alone retains version inference below the
// baseline so legacy Agents can still be upgraded, even without a declaration.
func Supports(name, agent string, declared Declaration) bool {
	if !buildinfo.MeetsAgentMinimum(agent) {
		return name == protocol.CapabilityAgentSelfUpdate && buildinfo.SupportsSelfUpdate(agent)
	}
	for _, g := range gates {
		if g.name == name {
			return slices.Contains(declared, name)
		}
	}
	return false
}

// AgentDeclaration advertises fixed protocol features independently of CLI
// readiness, and the eight CLI-dependent features only when supported by the
// detected CLI. The same declaration is used by native runtime checks.
func AgentDeclaration(geminiVersion, claudeVersion string) Declaration {
	declared := make(Declaration, 0, len(gates))
	for _, g := range gates {
		if g.cli == nil || g.cli(geminiVersion, claudeVersion) {
			declared = append(declared, g.name)
		}
	}
	return declared
}
