// Package capabilities resolves Agent declarations, with version inference only
// for legacy Agents that have not declared a set. Engine readiness, credentials
// and native probes remain separate from protocol support.
package capabilities

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

// Declaration distinguishes absent (nil, legacy inference) from explicitly
// empty (no gated capabilities). It is scanned from an optional TEXT side table
// and never included in the existing public HTTP response shapes.
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

type gate struct {
	name   string
	legacy func(string, string) bool
	cli    bool
}

var gates = []gate{
	{protocol.CapabilityAgentSelfUpdate, func(agent, cli string) bool { return buildinfo.SupportsSelfUpdate(agent) }, false},
	{protocol.CapabilityCodexCLIUpdate, func(agent, cli string) bool { return buildinfo.SupportsCodexUpdate(agent) }, false},
	{protocol.CapabilityDeployRollbackExact, func(agent, cli string) bool { return buildinfo.SupportsExactDeploymentRollback(agent) }, false},
	{protocol.CapabilityCodexSteer, func(agent, cli string) bool { return buildinfo.SupportsCodexSteer(agent) }, false},
	{protocol.CapabilityWorkspaceFileWrite, func(agent, cli string) bool { return buildinfo.SupportsWorkspaceFileWrite(agent) }, false},
	{protocol.CapabilityCodexForkAtMessage, func(agent, cli string) bool { return buildinfo.SupportsMessageFork(agent) }, false},
	{protocol.CapabilityGeminiSessions, func(agent, cli string) bool { return buildinfo.SupportsGemini(agent) }, false},
	{protocol.CapabilityCLIManage, func(agent, cli string) bool { return buildinfo.SupportsCLIManagement(agent) }, false},
	{protocol.CapabilityGeminiCredentials, func(agent, cli string) bool { return buildinfo.SupportsGeminiCredentials(agent) }, false},
	{protocol.CapabilityGeminiFork, func(agent, cli string) bool { return buildinfo.SupportsGeminiFork(agent) }, false},
	{protocol.CapabilityGeminiThinking, func(agent, cli string) bool { return buildinfo.SupportsGeminiThinking(agent) }, false},
	{protocol.CapabilityGeminiYolo, func(agent, cli string) bool { return buildinfo.SupportsGeminiYolo(agent) }, false},
	{protocol.CapabilityGeminiInput, func(agent, cli string) bool { return buildinfo.SupportsGeminiInput(agent) }, false},
	{protocol.CapabilityGeminiSteer, func(agent, cli string) bool { return buildinfo.SupportsGeminiSteering(agent, cli) }, true},
	{protocol.CapabilityGeminiSkills, func(agent, cli string) bool { return buildinfo.SupportsGeminiSkills(agent, cli) }, true},
	{protocol.CapabilityGeminiPlan, func(agent, cli string) bool { return buildinfo.SupportsGeminiPlan(agent, cli) }, true},
	{protocol.CapabilityGeminiGoal, func(agent, cli string) bool { return buildinfo.SupportsGeminiGoal(agent, cli) }, true},
	{protocol.CapabilityGeminiRewrite, func(agent, cli string) bool { return buildinfo.SupportsGeminiRewrite(agent) }, false},
	{protocol.CapabilityGeminiSteerImages, func(agent, cli string) bool { return buildinfo.SupportsGeminiImageSteering(agent, cli) }, true},
	{protocol.CapabilityGeminiSkillsManage, func(agent, cli string) bool { return buildinfo.SupportsGeminiSkillManagement(agent, cli) }, true},
	{protocol.CapabilityClaudeSessions, func(agent, cli string) bool { return buildinfo.SupportsClaude(agent) }, false},
	{protocol.CapabilityClaudeManage, func(agent, cli string) bool { return buildinfo.SupportsClaudeManagement(agent) }, false},
	{protocol.CapabilityClaudeEffort, func(agent, cli string) bool { return buildinfo.SupportsClaudeEffort(agent) }, false},
	{protocol.CapabilityClaudeInput, func(agent, cli string) bool { return buildinfo.SupportsClaudeInput(agent) }, false},
	{protocol.CapabilityClaudeSkills, func(agent, cli string) bool { return buildinfo.SupportsClaudeSkills(agent) }, false},
	{protocol.CapabilityClaudeSteer, func(agent, cli string) bool { return buildinfo.SupportsClaudeSteering(agent, cli) }, true},
	{protocol.CapabilityOperationInterrupt, func(agent, cli string) bool { return buildinfo.SupportsOperationInterrupt(agent) }, false},
	{protocol.CapabilityClaudeRewrite, func(agent, cli string) bool { return buildinfo.SupportsClaudeRewrite(agent) }, false},
	{protocol.CapabilityClaudeQuestions, func(agent, cli string) bool { return buildinfo.SupportsClaudeQuestions(agent, cli) }, true},
	{protocol.CapabilityCredentialsScoped, func(agent, cli string) bool { return buildinfo.SupportsScopedCredentials(agent) }, false},
	{protocol.CapabilitySessionCollaboration, func(agent, cli string) bool { return buildinfo.SupportsSessionCollaboration(agent) }, false},
}

// Supports treats a present declaration as authoritative, including an empty
// set. Unknown names never grant support, even when explicitly declared.
func Supports(name, agent, cli string, declared Declaration) bool {
	for _, g := range gates {
		if g.name != name {
			continue
		}
		if declared != nil {
			return slices.Contains(declared, name)
		}
		return g.legacy(agent, cli)
	}
	return false
}

// AgentDeclaration advertises fixed protocol features independently of CLI
// installation/readiness, and the eight CLI-dependent features only when the
// detected CLI version supports them. This matches legacy version inference.
func AgentDeclaration(geminiVersion, claudeVersion string) Declaration {
	declared := make(Declaration, 0, len(gates))
	for _, g := range gates {
		cli := geminiVersion
		if g.name == protocol.CapabilityClaudeSteer || g.name == protocol.CapabilityClaudeQuestions {
			cli = claudeVersion
		}
		if !g.cli || g.legacy(buildinfo.Version, cli) {
			declared = append(declared, g.name)
		}
	}
	return declared
}
