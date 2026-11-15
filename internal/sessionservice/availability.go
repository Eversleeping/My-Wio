package sessionservice

import (
	"context"

	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
)

type CreateDefaults struct {
	Model  string `json:"model"`
	Mode   string `json:"mode"`
	Effort string `json:"effort"`
}
type EngineAvailability struct {
	Engine       string               `json:"engine"`
	Available    bool                 `json:"available"`
	Reason       string               `json:"reason"`
	Capabilities session.Capabilities `json:"capabilities"`
	Defaults     CreateDefaults       `json:"defaults"`
}
type WorkspaceEngines struct {
	WorkspaceID string               `json:"workspace_id"`
	ServerID    string               `json:"server_id"`
	Engines     []EngineAvailability `json:"engines"`
}

// ResolveEngineAvailability combines feature support with current deployment
// state. Consumers render these reasons rather than implementing version gates.
func ResolveEngineAvailability(workspace store.Workspace, server store.Server) []EngineAvailability {
	result := []EngineAvailability{}
	for _, spec := range []struct {
		engine, version, profile string
		ready                    int
	}{
		{session.Codex, server.CodexVersion, server.CodexProfileID, server.CodexReady},
		{session.Gemini, server.GeminiVersion, server.GeminiProfileID, server.GeminiReady},
		{session.Claude, server.ClaudeVersion, server.ClaudeProfileID, server.ClaudeReady},
	} {
		item := EngineAvailability{Engine: spec.engine, Capabilities: session.ResolveCapabilities(spec.engine, server.AgentVersion, spec.version)}
		switch {
		case workspace.Status != "ready":
			item.Reason = "workspace_not_ready"
		case server.Status != "online":
			item.Reason = "server_offline"
		case !item.Capabilities.Turns:
			item.Reason = "agent_update"
		case spec.ready != 1:
			item.Reason = "cli_missing"
		case spec.profile == "":
			item.Reason = "credentials_missing"
		default:
			item.Available = true
		}
		result = append(result, item)
	}
	return result
}

func (s *Service) WorkspaceEngines(ctx context.Context, workspaceID string) (WorkspaceEngines, error) {
	workspace, err := s.Store.Workspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceEngines{}, err
	}
	server, err := s.Store.Server(ctx, workspace.ServerID)
	if err != nil {
		return WorkspaceEngines{}, err
	}
	defaults, err := s.Store.WorkspaceSessionDefaults(ctx, workspaceID)
	if err != nil {
		return WorkspaceEngines{}, err
	}
	result := WorkspaceEngines{WorkspaceID: workspaceID, ServerID: server.ID, Engines: ResolveEngineAvailability(workspace, server)}
	for i := range result.Engines {
		row := defaults[result.Engines[i].Engine]
		result.Engines[i].Defaults = CreateDefaults{Model: row.Model, Mode: row.Mode, Effort: row.Effort}
	}
	return result, nil
}
