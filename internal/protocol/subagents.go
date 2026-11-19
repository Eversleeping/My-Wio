package protocol

import "encoding/json"

// SubagentUpdate belongs to the parent Wio stream. Metadata and content entries
// are separate events, so listing agents never loads their transcripts.
type SubagentUpdate struct {
	ID          string         `json:"id"`
	ParentID    string         `json:"parent_id,omitempty"`
	NativeID    string         `json:"native_id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Status      string         `json:"status,omitempty"`
	Prompt      string         `json:"prompt,omitempty"`
	Model       string         `json:"model,omitempty"`
	Detail      string         `json:"detail,omitempty"`
	OperationID string         `json:"operation_id,omitempty"`
	Entry       *SubagentEntry `json:"entry,omitempty"`
}

type SubagentEntry struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Text   string          `json:"text,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Status string          `json:"status,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}
