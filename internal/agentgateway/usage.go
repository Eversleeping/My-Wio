package agentgateway

import "encoding/json"

// Token usage notifications contain a field whose name also matches secret
// redaction. Keep only the documented numeric counters, never arbitrary data.
func codexUsagePayload(raw json.RawMessage) json.RawMessage {
	var notification struct {
		Usage struct {
			Total struct {
				Input  *int64 `json:"inputTokens"`
				Output *int64 `json:"outputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(raw, &notification) != nil {
		return json.RawMessage(`{}`)
	}
	counts := make(map[string]int64)
	if v := notification.Usage.Total.Input; v != nil && *v >= 0 {
		counts["input_tokens"] = *v
	}
	if v := notification.Usage.Total.Output; v != nil && *v >= 0 {
		counts["output_tokens"] = *v
	}
	result, _ := json.Marshal(map[string]any{"usage": counts})
	return result
}
