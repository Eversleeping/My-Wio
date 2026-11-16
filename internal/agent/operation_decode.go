package agent

import "encoding/json"

// decodeOperationCommand deliberately accepts unknown fields. The control plane
// can add optional command fields while older Agents keep their native behavior.
func decodeOperationCommand[T any](raw json.RawMessage, command *T) error {
	return json.Unmarshal(raw, command)
}
