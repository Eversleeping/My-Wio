package session

import "github.com/wio-platform/wio/internal/buildinfo"

// Capabilities describe version support, independent of current session state,
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

// ResolveCapabilities is the sole mapping from engine and version strings to
// session features. Existing buildinfo gates retain their exact version rules.
func ResolveCapabilities(engine Engine, agentVersion, cliVersion string) Capabilities {
	var c Capabilities
	switch engine {
	case Codex:
		c = Capabilities{
			Turns:        true,
			Images:       true,
			Steer:        buildinfo.SupportsCodexSteer(agentVersion),
			Rewrite:      true,
			Fork:         true,
			ForkMessages: buildinfo.SupportsMessageFork(agentVersion),
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
			Turns:          buildinfo.SupportsGemini(agentVersion),
			Images:         buildinfo.SupportsGeminiInput(agentVersion),
			Steer:          buildinfo.SupportsGeminiSteering(agentVersion, cliVersion),
			SteerImages:    buildinfo.SupportsGeminiImageSteering(agentVersion, cliVersion),
			Rewrite:        buildinfo.SupportsGeminiRewrite(agentVersion),
			Fork:           buildinfo.SupportsGeminiFork(agentVersion),
			Questions:      true,
			Approvals:      true,
			Goal:           buildinfo.SupportsGeminiGoal(agentVersion, cliVersion),
			Plan:           buildinfo.SupportsGeminiPlan(agentVersion, cliVersion),
			Skills:         buildinfo.SupportsGeminiSkills(agentVersion, cliVersion),
			SkillsManage:   buildinfo.SupportsGeminiSkillManagement(agentVersion, cliVersion),
			Thinking:       buildinfo.SupportsGeminiThinking(agentVersion),
			Yolo:           buildinfo.SupportsGeminiYolo(agentVersion),
			ConfirmStopped: true,
		}
		c.ForkMessages, c.ForkWorktree = c.Fork, c.Fork
	case Claude:
		c = Capabilities{
			Turns:          buildinfo.SupportsClaude(agentVersion),
			Images:         buildinfo.SupportsClaudeInput(agentVersion),
			Steer:          buildinfo.SupportsClaudeSteering(agentVersion, cliVersion),
			Rewrite:        buildinfo.SupportsClaudeRewrite(agentVersion),
			Fork:           buildinfo.SupportsClaudeInput(agentVersion),
			Questions:      buildinfo.SupportsClaudeQuestions(agentVersion, cliVersion),
			Approvals:      true,
			Compact:        true,
			Skills:         buildinfo.SupportsClaudeSkills(agentVersion),
			Effort:         buildinfo.SupportsClaudeEffort(agentVersion),
			Yolo:           true,
			ConfirmStopped: true,
		}
		c.SteerImages, c.ForkMessages, c.ForkWorktree = c.Steer, c.Fork, c.Fork
	default:
		return c
	}
	c.Collaboration = buildinfo.SupportsSessionCollaboration(agentVersion)
	return c
}
