//go:build darwin && platformintegration

package installer

import (
	"github.com/wio-platform/wio/internal/hostplatform"
	"testing"
)

func nativeServiceDiagnostics(*testing.T, hostplatform.Layout) {}

func nativeDependencyProbe(*testing.T, hostplatform.Layout, []byte) (string, bool) {
	return "wio-no-test-cli", false
}
