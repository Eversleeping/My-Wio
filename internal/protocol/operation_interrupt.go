package protocol

type OperationInterruptCommand struct {
	OperationID string `json:"operation_id"`
	Kind        string `json:"kind"`
}

// Only synchronous, read-only operations whose lifetime follows their context.
func CanInterruptOperation(kind string) bool {
	switch kind {
	case "git.workspace.inspect", "inventory.scan", "workspace.files", "workspace.changes", "workspace.file.preview", "workspace.diff.preview":
		return true
	}
	return false
}
