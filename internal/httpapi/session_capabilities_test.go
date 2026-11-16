package httpapi

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

func TestUnifiedCapabilityObjectReplacesLegacyFields(t *testing.T) {
	for _, engine := range []string{"codex", "gemini", "claude"} {
		t.Run(engine, func(t *testing.T) {
			f := newSessionContract(t, engine)
			for _, version := range []string{"0.2.50", "0.2.79", "0.2.94", "0.2.98"} {
				if err := f.db.Heartbeat(context.Background(), f.serverID, protocol.Heartbeat{AgentVersion: version, GeminiVersion: "0.66.0", ClaudeVersion: "2.1.286"}); err != nil {
					t.Fatal(err)
				}
				detail := f.call("GET", f.path(""), "", 200)
				w := f.request("GET", "/api/sessions?engine="+engine, "")
				var page struct {
					Items []map[string]any `json:"items"`
				}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
					t.Fatalf("list: %s", w.Body)
				}
				for _, item := range []map[string]any{detail, page.Items[0]} {
					caps, ok := item["capabilities"].(map[string]any)
					if !ok {
						t.Fatal("missing capabilities", item)
					}
					fields := map[string]string{"steer_supported": "steer", "steer_images_supported": "steer_images", "skills_management_supported": "skills_manage", "input_supported": "images", "thinking_supported": "thinking", "skills_supported": "skills", "goal_supported": "goal", "plan_supported": "plan", "fork_supported": "fork", "rewrite_supported": "rewrite", "questions_supported": "questions", "effort_supported": "effort"}
					driver, err := f.db.AISession(context.Background(), session.Ref{Engine: engine, ID: f.id})
					if err != nil {
						t.Fatal(err)
					}
					raw, _ := json.Marshal(driver.Capabilities)
					var expected map[string]any
					if err := json.Unmarshal(raw, &expected); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(caps, expected) {
						t.Fatalf("%s %s capabilities: %v want %v", engine, version, caps, expected)
					}
					for old := range fields {
						if _, ok := item[old]; ok {
							t.Fatalf("retired field %s leaked", old)
						}
					}
				}
			}
		})
	}
}
