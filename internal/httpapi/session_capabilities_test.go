package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestLegacyCapabilityFieldsMatchCapabilityObject(t *testing.T) {
	for _, engine := range []string{"codex", "gemini", "claude"} {
		t.Run(engine, func(t *testing.T) {
			f := newSessionContract(t, engine)
			for _, version := range []string{"0.2.50", "0.2.79", "0.2.94", "0.2.98"} {
				if err := f.db.Heartbeat(context.Background(), f.serverID, protocol.Heartbeat{AgentVersion: version, GeminiVersion: "0.66.0", ClaudeVersion: "2.1.286"}); err != nil {
					t.Fatal(err)
				}
				detail := f.call("GET", f.path(""), "", 200)
				w := f.request("GET", f.prefix, "")
				var items []map[string]any
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != 1 {
					t.Fatalf("list: %s", w.Body)
				}
				for _, item := range []map[string]any{detail, items[0]} {
					caps, ok := item["capabilities"].(map[string]any)
					if !ok {
						t.Fatal("missing capabilities", item)
					}
					fields := map[string]string{"steer_supported": "steer", "steer_images_supported": "steer_images", "skills_management_supported": "skills_manage", "input_supported": "images", "thinking_supported": "thinking", "skills_supported": "skills", "goal_supported": "goal", "plan_supported": "plan", "fork_supported": "fork", "rewrite_supported": "rewrite", "questions_supported": "questions", "effort_supported": "effort"}
					for old, current := range fields {
						if value, ok := item[old]; ok && value != caps[current] {
							t.Fatalf("%s %s: %s=%v, %s=%v", engine, version, old, value, current, caps[current])
						}
					}
				}
			}
		})
	}
}
