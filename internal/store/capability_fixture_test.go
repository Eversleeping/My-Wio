package store

import (
	"github.com/wio-platform/wio/internal/capabilities"
	"slices"
)

func testCapabilities(gemini, claude string, omitted ...string) capabilities.Declaration {
	return slices.DeleteFunc(capabilities.AgentDeclaration(gemini, claude), func(name string) bool { return slices.Contains(omitted, name) })
}
