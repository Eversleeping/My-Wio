package protocol

type OperationInterruptCommand struct {
	OperationID string `json:"operation_id"`
	Kind        string `json:"kind"`
}

// Only synchronous, read-only operations whose lifetime follows their context.
func CanInterruptOperation(kind string) bool {
	switch kind {
	case OpGitWorkspaceInspect, OpInventoryScan, OpWorkspaceFiles, OpWorkspaceChanges, OpWorkspaceFilePreview, OpWorkspaceDiffPreview:
		return true
	}
	return false
}
