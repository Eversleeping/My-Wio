package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

// These producers were not covered by the existing models-refresh test. Keep
// their scope and optional goal fields fixed before changing Agent dispatch.
func TestCodexSnapshotProducersPreserveWireScope(t *testing.T) {
	for _, kind := range []string{protocol.OpCodexGoalGet, protocol.OpCodexGoalSet, protocol.OpCodexGoalClear, protocol.OpCodexStatusSnapshot, protocol.OpCodexMCPList, protocol.OpCodexSkillsList} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			db := openBootstrapTestStore(t)
			server := enrollResourceTestServer(t, db, "wire-scope")
			if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.3.26", Capabilities: testCapabilities("", ""), CodexVersion: "0.159.3", CodexReady: true}); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
				t.Fatal(err)
			}
			workspaces, err := db.ListWorkspaces(ctx)
			if err != nil || len(workspaces) != 1 {
				t.Fatalf("workspaces: %v %v", workspaces, err)
			}
			workspace := workspaces[0]
			thread, err := db.CreateThread(ctx, workspace.ID, "Wire scope")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET codex_thread_id=? WHERE id=?"), "native-thread", thread.ID); err != nil {
				t.Fatal(err)
			}
			api := resourceTestAPI(db)
			scope, scopeID := "thread", thread.ID
			handlers := map[string]http.HandlerFunc{protocol.OpCodexGoalGet: api.refreshThreadGoal, protocol.OpCodexGoalSet: api.setThreadGoal, protocol.OpCodexGoalClear: api.clearThreadGoal, protocol.OpCodexStatusSnapshot: api.refreshThreadCodexStatus}
			var body any = map[string]any{}
			if kind == protocol.OpCodexGoalSet {
				body = map[string]any{"objective": "Finish the fixture", "status": "paused", "token_budget": int64(1200)}
			}
			if handler, ok := handlers[kind]; ok {
				response := threadResourceRequest(t, http.MethodPost, "/", thread.ID, body, handler)
				if response.Code != http.StatusAccepted {
					t.Fatalf("%d: %s", response.Code, response.Body.String())
				}
			} else {
				scope, scopeID = "workspace", workspace.ID
				handler := api.refreshWorkspaceCodexMCP
				if kind == protocol.OpCodexSkillsList {
					handler = api.refreshWorkspaceCodexSkills
				}
				response := workspaceResourceRequest(t, http.MethodPost, "/", workspace.ID, body, handler)
				if response.Code != http.StatusAccepted {
					t.Fatalf("%d: %s", response.Code, response.Body.String())
				}
			}
			operations, err := db.PendingOperations(ctx, server.ID)
			if err != nil || len(operations) != 1 {
				t.Fatalf("operations: %v %v", operations, err)
			}
			op := operations[0]
			var command protocol.CodexGoalSetCommand
			if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
				t.Fatal(err)
			}
			if op.Kind != kind || command.ScopeType != scope || command.ScopeID != scopeID || command.Workspace != workspace.Path || command.CodexVersion != "0.159.3" {
				t.Fatalf("wrong wire scope: %s %+v", op.Kind, command)
			}
			if scope == "thread" && (command.ThreadID != thread.ID || command.CodexThread != "native-thread") {
				t.Fatalf("lost native binding: %+v", command)
			}
			if scope == "workspace" && (command.ThreadID != "" || command.CodexThread != "") {
				t.Fatalf("workspace gained thread scope: %+v", command)
			}
			if kind == protocol.OpCodexGoalSet && (command.Objective == nil || *command.Objective != "Finish the fixture" || command.Status == nil || *command.Status != "paused" || command.TokenBudget == nil || *command.TokenBudget != 1200) {
				t.Fatalf("lost goal options: %+v", command)
			}
		})
	}
}
