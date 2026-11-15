package agentgateway

import (
	"encoding/json"
)

func scheduledTurnStatus(status string) string {
	switch status {
	case "idle":
		return "succeeded"
	case "interrupted":
		return "cancelled"
	default:
		return "failed"
	}
}

func scheduledTurnMessage(payload json.RawMessage) string {
	var value struct {
		Error   json.RawMessage `json:"error"`
		Text    string          `json:"text"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	if value.Text != "" {
		return value.Text
	}
	if value.Message != "" {
		return value.Message
	}
	var errorText string
	if json.Unmarshal(value.Error, &errorText) == nil && errorText != "" {
		return errorText
	}
	var detail struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(value.Error, &detail) == nil {
		return detail.Message
	}
	return ""
}
