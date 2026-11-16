package session

import (
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"testing"
)

func TestDeclaredSessionCapabilities(t *testing.T) {
	for _, gemini := range []string{"", "invalid", "0.65.99", "0.66.0", "0.66.1"} {
		for _, claude := range []string{"", "invalid", "2.1.285", "2.1.286", "2.1.286 (Claude Code)"} {
			declared := capabilities.AgentDeclaration(gemini, claude)
			for engine, cli := range map[string]string{Codex: "", Gemini: gemini, Claude: claude} {
				expected := ResolveCapabilities(engine, buildinfo.Version, cli, declared)
				if got := ResolveCapabilities(engine, buildinfo.MinimumAgentVersion, "invalid", declared); got != expected {
					t.Fatalf("engine=%s Gemini=%q Claude=%q: %+v want %+v", engine, gemini, claude, got, expected)
				}
			}
		}
	}
	for _, engine := range []string{Codex, Gemini, Claude} {
		c := ResolveCapabilities(engine, "9.9.9", "9.9.9", capabilities.Declaration{})
		if c.Steer || c.Collaboration || c.ForkMessages || c.SkillsManage {
			t.Fatalf("version enabled undeclared features: %+v", c)
		}
		c = ResolveCapabilities(engine, buildinfo.MinimumAgentVersion, "", capabilities.Declaration{protocol.CapabilitySessionCollaboration})
		if !c.Collaboration {
			t.Fatal("declaration lost")
		}
	}
}
