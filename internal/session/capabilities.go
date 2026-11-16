package session

import (
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
)

// Capabilities describe protocol support, independent of current session state,
// Agent connectivity, credential availability and native CLI feature probes.
type Capabilities struct {
	Turns          bool `json:"turns"`
	Images         bool `json:"images"`
	Steer          bool `json:"steer"`
	SteerImages    bool `json:"steer_images"`
	Rewrite        bool `json:"rewrite"`
	Fork           bool `json:"fork"`
	ForkMessages   bool `json:"fork_messages"`
	ForkWorktree   bool `json:"fork_worktree"`
	Questions      bool `json:"questions"`
	Approvals      bool `json:"approvals"`
	Goal           bool `json:"goal"`
	Plan           bool `json:"plan"`
	Compact        bool `json:"compact"`
	Skills         bool `json:"skills"`
	SkillsManage   bool `json:"skills_manage"`
	Effort         bool `json:"effort"`
	Thinking       bool `json:"thinking"`
	Yolo           bool `json:"yolo"`
	ConfirmStopped bool `json:"confirm_stopped"`
	Collaboration  bool `json:"collaboration"`
}

// ResolveCapabilities maps declarations to session features. Undeclared legacy
// Agents retain the exact previous version rules. Ungated features stay static.
func ResolveCapabilities(engine Engine, agentVersion, cliVersion string, declarations ...capabilities.Declaration) Capabilities {
	var declared capabilities.Declaration
	if len(declarations) > 0 {
		declared = declarations[0]
	}
	supports := func(name string) bool { return capabilities.Supports(name, agentVersion, cliVersion, declared) }
	var c Capabilities
	switch engine {
	case Codex:
		c = Capabilities{
			Turns:        true,
			Images:       true,
			Steer:        supports(protocol.CapabilityCodexSteer),
			Rewrite:      true,
			Fork:         true,
			ForkMessages: supports(protocol.CapabilityCodexForkAtMessage),
			ForkWorktree: true,
			Questions:    true,
			Approvals:    true,
			Goal:         true,
			Compact:      true,
			Skills:       true,
			Effort:       true,
			Yolo:         true,
		}
		c.SteerImages = c.Steer
	case Gemini:
		c = Capabilities{
			Turns:          supports(protocol.CapabilityGeminiSessions),
			Images:         supports(protocol.CapabilityGeminiInput),
			Steer:          supports(protocol.CapabilityGeminiSteer),
			SteerImages:    supports(protocol.CapabilityGeminiSteerImages),
			Rewrite:        supports(protocol.CapabilityGeminiRewrite),
			Fork:           supports(protocol.CapabilityGeminiFork),
			Questions:      true,
			Approvals:      true,
			Goal:           supports(protocol.CapabilityGeminiGoal),
			Plan:           supports(protocol.CapabilityGeminiPlan),
			Skills:         supports(protocol.CapabilityGeminiSkills),
			SkillsManage:   supports(protocol.CapabilityGeminiSkillsManage),
			Thinking:       supports(protocol.CapabilityGeminiThinking),
			Yolo:           supports(protocol.CapabilityGeminiYolo),
			ConfirmStopped: true,
		}
		c.ForkMessages, c.ForkWorktree = c.Fork, c.Fork
	case Claude:
		c = Capabilities{
			Turns:          supports(protocol.CapabilityClaudeSessions),
			Images:         supports(protocol.CapabilityClaudeInput),
			Steer:          supports(protocol.CapabilityClaudeSteer),
			Rewrite:        supports(protocol.CapabilityClaudeRewrite),
			Fork:           supports(protocol.CapabilityClaudeInput),
			Questions:      supports(protocol.CapabilityClaudeQuestions),
			Approvals:      true,
			Compact:        true,
			Skills:         supports(protocol.CapabilityClaudeSkills),
			Effort:         supports(protocol.CapabilityClaudeEffort),
			Yolo:           true,
			ConfirmStopped: true,
		}
		c.SteerImages, c.ForkMessages, c.ForkWorktree = c.Steer, c.Fork, c.Fork
	default:
		return c
	}
	c.Collaboration = supports(protocol.CapabilitySessionCollaboration)
	return c
}
