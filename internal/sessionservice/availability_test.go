package sessionservice

import (
	"testing"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/store"
)

func TestResolveEngineAvailability(t *testing.T) {
	ready := store.Server{DeclaredCapabilities: capabilities.AgentDeclaration("0.66.0", "2.1.318"), Status: "online", AgentVersion: buildinfo.Version, CodexReady: 1, GeminiReady: 1, ClaudeReady: 1, CodexProfileID: "c", GeminiProfileID: "g", ClaudeProfileID: "a", GeminiVersion: "0.66.0", ClaudeVersion: "2.1.318"}
	for _, tc := range []struct {
		name, workspaceStatus string
		change                func(*store.Server)
		want                  []string
	}{
		{"ready", "ready", func(*store.Server) {}, []string{"", "", ""}},
		{"workspace", "pending", func(*store.Server) {}, []string{"workspace_not_ready", "workspace_not_ready", "workspace_not_ready"}},
		{"offline", "ready", func(s *store.Server) { s.Status = "offline" }, []string{"server_offline", "server_offline", "server_offline"}},
		{"agent", "ready", func(s *store.Server) { s.AgentVersion = "0.1.0" }, []string{"agent_update", "agent_update", "agent_update"}},
		{"CLI", "ready", func(s *store.Server) { s.GeminiReady = 0 }, []string{"", "cli_missing", ""}},
		{"credentials", "ready", func(s *store.Server) { s.ClaudeProfileID = "" }, []string{"", "", "credentials_missing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ready
			tc.change(&s)
			got := ResolveEngineAvailability(store.Workspace{Status: tc.workspaceStatus}, s)
			for i, item := range got {
				if item.Reason != tc.want[i] || item.Available != (tc.want[i] == "") {
					t.Fatalf("%s: %+v, want %s", item.Engine, item, tc.want[i])
				}
			}
		})
	}
}
