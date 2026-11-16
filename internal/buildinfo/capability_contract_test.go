package buildinfo

import (
	"fmt"
	"testing"
)

// Frozen compatibility contract before P4-02 replaces control-plane gates.
func TestCapabilityGateContract(t *testing.T) {
	gates := []struct {
		name, minimum, cli string
		supports           func(string, string) bool
	}{
		{"agent.self_update", "0.2.0", "", func(agent, cli string) bool { return SupportsSelfUpdate(agent) }},
		{"codex.cli_update", "0.2.9", "", func(agent, cli string) bool { return SupportsCodexUpdate(agent) }},
		{"deploy.rollback_exact", "0.2.45", "", func(agent, cli string) bool { return SupportsExactDeploymentRollback(agent) }},
		{"codex.steer", "0.2.51", "", func(agent, cli string) bool { return SupportsCodexSteer(agent) }},
		{"workspace.file_write", "0.2.56", "", func(agent, cli string) bool { return SupportsWorkspaceFileWrite(agent) }},
		{"codex.fork_at_message", "0.2.59", "", func(agent, cli string) bool { return SupportsMessageFork(agent) }},
		{"gemini.sessions", "0.2.60", "", func(agent, cli string) bool { return SupportsGemini(agent) }},
		{"cli.manage", "0.2.61", "", func(agent, cli string) bool { return SupportsCLIManagement(agent) }},
		{"gemini.credentials", "0.2.63", "", func(agent, cli string) bool { return SupportsGeminiCredentials(agent) }},
		{"gemini.fork", "0.2.68", "", func(agent, cli string) bool { return SupportsGeminiFork(agent) }},
		{"gemini.thinking", "0.2.70", "", func(agent, cli string) bool { return SupportsGeminiThinking(agent) }},
		{"gemini.yolo", "0.2.72", "", func(agent, cli string) bool { return SupportsGeminiYolo(agent) }},
		{"gemini.input", "0.2.73", "", func(agent, cli string) bool { return SupportsGeminiInput(agent) }},
		{"gemini.steer", "0.2.79", "0.66.0", func(agent, cli string) bool { return SupportsGeminiSteering(agent, cli) }},
		{"gemini.skills", "0.2.83", "0.66.0", func(agent, cli string) bool { return SupportsGeminiSkills(agent, cli) }},
		{"gemini.plan", "0.2.83", "0.66.0", func(agent, cli string) bool { return SupportsGeminiPlan(agent, cli) }},
		{"gemini.goal", "0.2.84", "0.66.0", func(agent, cli string) bool { return SupportsGeminiGoal(agent, cli) }},
		{"gemini.rewrite", "0.2.85", "", func(agent, cli string) bool { return SupportsGeminiRewrite(agent) }},
		{"gemini.steer_images", "0.2.88", "0.66.0", func(agent, cli string) bool { return SupportsGeminiImageSteering(agent, cli) }},
		{"gemini.skills_manage", "0.2.88", "0.66.0", func(agent, cli string) bool { return SupportsGeminiSkillManagement(agent, cli) }},
		{"claude.sessions", "0.2.90", "", func(agent, cli string) bool { return SupportsClaude(agent) }},
		{"claude.manage", "0.2.91", "", func(agent, cli string) bool { return SupportsClaudeManagement(agent) }},
		{"claude.effort", "0.2.92", "", func(agent, cli string) bool { return SupportsClaudeEffort(agent) }},
		{"claude.input", "0.2.93", "", func(agent, cli string) bool { return SupportsClaudeInput(agent) }},
		{"claude.skills", "0.2.94", "", func(agent, cli string) bool { return SupportsClaudeSkills(agent) }},
		{"claude.steer", "0.2.95", "2.1.286", func(agent, cli string) bool { return SupportsClaudeSteering(agent, cli) }},
		{"operation.interrupt", "0.2.96", "", func(agent, cli string) bool { return SupportsOperationInterrupt(agent) }},
		{"claude.rewrite", "0.2.97", "", func(agent, cli string) bool { return SupportsClaudeRewrite(agent) }},
		{"claude.questions", "0.2.98", "2.1.286", func(agent, cli string) bool { return SupportsClaudeQuestions(agent, cli) }},
		{"credentials.scoped", "0.3.02", "", func(agent, cli string) bool { return SupportsScopedCredentials(agent) }},
		{"session.collaboration", "0.3.06", "", func(agent, cli string) bool { return SupportsSessionCollaboration(agent) }},
	}
	for _, g := range gates {
		t.Run(g.name, func(t *testing.T) {
			var major, minor, patch int
			if _, err := fmt.Sscanf(g.minimum, "%d.%d.%d", &major, &minor, &patch); err != nil {
				t.Fatal(err)
			}
			below := fmt.Sprintf("%d.%d.%d", major, minor, patch-1)
			if patch == 0 {
				below = "0.1.999"
			}
			for _, agent := range []string{"", "invalid", below} {
				if g.supports(agent, g.cli) {
					t.Fatalf("accepted old/invalid Agent %q", agent)
				}
			}
			for _, agent := range []string{g.minimum, "v" + g.minimum + "+build", "1.0.0"} {
				if !g.supports(agent, g.cli) {
					t.Fatalf("rejected compatible Agent %q", agent)
				}
			}
			if g.cli != "" {
				oldCLI := "0.65.99"
				if g.cli == "2.1.286" {
					oldCLI = "2.1.285"
				}
				for _, cli := range []string{"", "invalid", oldCLI} {
					if g.supports("1.0.0", cli) {
						t.Fatalf("accepted incompatible CLI %q", cli)
					}
				}
				if g.cli == "2.1.286" && !g.supports("1.0.0", g.cli+" (Claude Code)") {
					t.Fatal("rejected Claude version suffix")
				}
			}
		})
	}
}
