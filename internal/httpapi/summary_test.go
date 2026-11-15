package httpapi

import (
	"context"
	"testing"
)

func TestSummarySessionCount(t *testing.T) {
	f := newSessionContract(t, "codex")
	ctx := context.Background()
	for range 2 {
		if _, err := f.db.CreateGeminiThread(ctx, f.workspaceID, "auto", "default"); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if _, err := f.db.CreateClaudeThread(ctx, f.workspaceID, "sonnet", "default", ""); err != nil {
			t.Fatal(err)
		}
	}
	// The existing counter includes archived sessions; widening its engine scope
	// must preserve that behavior and its published field name.
	f.call("PATCH", f.path(""), `{"archived":true}`, 200)
	result := f.call("GET", "/api/summary", "", 200)
	counts, ok := result["counts"].(map[string]any)
	if !ok || counts["threads"] != float64(1) {
		t.Fatalf("counts: %#v", result)
	}
	for _, key := range []string{"servers", "online", "projects", "deployments", "alerts"} {
		if _, ok := counts[key]; !ok {
			t.Fatalf("missing count %s: %#v", key, counts)
		}
	}
	for _, key := range []string{"deployments", "alerts"} {
		if value, ok := result[key]; !ok || value != nil {
			t.Fatalf("%s empty result changed: %#v", key, result)
		}
	}
}
