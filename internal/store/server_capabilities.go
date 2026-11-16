package store

import (
	"github.com/wio-platform/wio/internal/capabilities"
)

// Each use aliases the joined server as s. An absent row scans as nil and
// grants no features; an explicit [] declaration stays authoritative.
const serverCapabilitySelect = "(SELECT capabilities FROM server_capabilities WHERE server_id=s.id) AS declared_capabilities"

func (s Server) Supports(name string) bool {
	return capabilities.Supports(name, s.AgentVersion, s.DeclaredCapabilities)
}
