package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/subagent"
)

func TestSubagentRoutesRequireAuthenticationAndParentMembership(t *testing.T) {
	for _, engine := range []string{"codex", "claude", "gemini"} {
		t.Run(engine, func(t *testing.T) {
			f := newSessionContract(t, engine)
			ctx := context.Background()
			for _, update := range []protocol.SubagentUpdate{{ID: "child", Name: "Review", Status: "running", Detail: "activity"}, {ID: "child", Entry: &protocol.SubagentEntry{ID: "answer", Kind: "message", Text: "Review result"}}} {
				if _, err := f.db.AddEvent(ctx, subagent.Event(f.id, update)); err != nil {
					t.Fatal(err)
				}
			}
			path := f.path("/subagents")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 401 {
				t.Fatalf("unauthenticated content: %d", w.Code)
			}
			list := f.call("GET", path, "", 200)
			if len(list["items"].([]any)) != 1 {
				t.Fatal(list)
			}
			page := f.call("GET", path+"/child/events", "", 200)
			if len(page["items"].([]any)) != 1 {
				t.Fatal(page)
			}
			cursor := page["next"].(float64)
			next := f.call("GET", fmt.Sprintf("%s/child/events?after=%.0f", path, cursor), "", 200)
			if len(next["items"].([]any)) != 0 {
				t.Fatal("cursor duplicated an entry")
			}
			f.call("GET", path+"/child/events?after=-1", "", 400)
			f.call("GET", path+"/another-child/events", "", 404)
			f.call("GET", f.prefix+"/missing/subagents/child/events", "", 404)
		})
	}
}
