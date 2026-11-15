package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessionservice"
)

func TestWorkspaceSessionEngines(t *testing.T) {
	f := newSessionContract(t, "codex")
	ctx := context.Background()
	if _, err := f.db.CreateGeminiThread(ctx, f.workspaceID, "flash", "yolo", "high"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CreateClaudeThread(ctx, f.workspaceID, "opus", "acceptEdits", "max"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.UpsertInventory(ctx, f.serverID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/contract", Name: "contract"}, {Path: "/srv/other", Name: "other"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := f.db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other := ""
	for _, w := range workspaces {
		if w.ID != f.workspaceID {
			other = w.ID
		}
	}
	if _, err = f.db.CreateGeminiThread(ctx, other, "other-model", "default", "low"); err != nil {
		t.Fatal(err)
	}
	for _, wid := range []string{f.workspaceID, other} {
		response := f.request("GET", "/api/workspaces/"+wid+"/session-engines", "")
		var result sessionservice.WorkspaceEngines
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatal(response.Code, response.Body.String())
		}
		if result.WorkspaceID != wid || result.ServerID != f.serverID || len(result.Engines) != 3 {
			t.Fatal(result)
		}
		for _, item := range result.Engines {
			if item.Engine == "gemini" {
				want := "flash"
				if wid == other {
					want = "other-model"
				}
				if item.Defaults.Model != want {
					t.Fatalf("cross-workspace defaults: %+v", result)
				}
			}
		}
	}
	if response := f.request("GET", "/api/workspaces/missing/session-engines", ""); response.Code != 404 {
		t.Fatal(response.Code, response.Body.String())
	}
	r := httptest.NewRequest("GET", "/api/workspaces/"+f.workspaceID+"/session-engines", nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("unauthenticated engines: %d", w.Code)
	}
}
