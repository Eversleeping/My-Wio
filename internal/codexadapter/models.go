package codexadapter

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/wio-platform/wio/internal/protocol"
)

// Model metadata is allowlisted; credentials and provider configuration never
// become part of a browser snapshot.
type codexModel struct {
	ID                        string                 `json:"id"`
	Model                     string                 `json:"model"`
	DisplayName               string                 `json:"displayName"`
	Description               string                 `json:"description"`
	Hidden                    bool                   `json:"hidden"`
	IsDefault                 bool                   `json:"isDefault"`
	DefaultReasoningEffort    string                 `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []codexReasoningEffort `json:"supportedReasoningEfforts"`
}

type codexReasoningEffort struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description"`
}

func codexModelsOperation(ctx context.Context, version string, request requestFunc) (protocol.CodexCapabilityResult, error) {
	models := []codexModel{}
	seenModels := map[string]bool{}
	seenCursors := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := request(ctx, "model/list", params)
		if err != nil {
			if isUnsupportedRPC(err) {
				return unsupported(version, "This Codex version does not support model/list"), nil
			}
			return protocol.CodexCapabilityResult{}, err
		}
		var response struct {
			Data       []codexModel `json:"data"`
			NextCursor *string      `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return protocol.CodexCapabilityResult{}, err
		}
		if response.Data == nil {
			return protocol.CodexCapabilityResult{}, errors.New("invalid model/list response: missing data")
		}
		for _, model := range response.Data {
			if model.Model == "" {
				model.Model = model.ID
			}
			if model.Hidden || model.Model == "" || seenModels[model.Model] {
				continue
			}
			if model.DisplayName == "" {
				model.DisplayName = model.Model
			}
			seenModels[model.Model] = true
			models = append(models, model)
		}
		if response.NextCursor == nil || *response.NextCursor == "" {
			data, err := json.Marshal(models)
			return protocol.CodexCapabilityResult{Supported: true, CodexVersion: version, Data: data}, err
		}
		cursor = *response.NextCursor
		if seenCursors[cursor] {
			return protocol.CodexCapabilityResult{}, errors.New("model/list returned a repeated cursor")
		}
		seenCursors[cursor] = true
	}
	return protocol.CodexCapabilityResult{}, errors.New("model/list exceeded pagination limit")
}
