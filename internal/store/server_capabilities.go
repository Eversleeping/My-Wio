package store

import (
	"github.com/wio-platform/wio/internal/capabilities"
	"strings"
)

// Each use aliases the joined server as s. An absent row scans as nil and
// selects legacy inference; an explicit [] declaration stays authoritative.
const serverCapabilitySelect = "(SELECT capabilities FROM server_capabilities WHERE server_id=s.id) AS declared_capabilities"

func (s Server) Supports(name string) bool {
	cli := s.CodexVersion
	if strings.HasPrefix(name, "gemini.") {
		cli = s.GeminiVersion
	}
	if strings.HasPrefix(name, "claude.") {
		cli = s.ClaudeVersion
	}
	return capabilities.Supports(name, s.AgentVersion, cli, s.DeclaredCapabilities)
}
