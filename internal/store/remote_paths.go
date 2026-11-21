package store

import (
	"github.com/wio-platform/wio/internal/hostplatform"
	"strings"
)

func insideManagedWorkspaceRoot(workspacePath string, roots []string) bool {
	workspacePath = strings.TrimSpace(workspacePath)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if !hostplatform.Equal(root, workspacePath) && hostplatform.Contains(root, workspacePath) {
			return true
		}
	}
	return false
}
