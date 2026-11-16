package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestSessionStartPreparationOrder(t *testing.T) {
	for _, kind := range []string{protocol.OpCodexTurnStart, protocol.OpCodexTurnRewrite, protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite, protocol.OpClaudeTurnStart} {
		t.Run(kind+"/update_before_decode", func(t *testing.T) {
			client := &Client{updatePending: true}
			err := client.execute(context.Background(), &protocol.ControlEnvelope{Kind: kind, PayloadJSON: json.RawMessage(`{`)})
			if err == nil || err.Error() != "Agent update is in progress; retry the task after it reconnects" {
				t.Fatalf("update must precede decoding: %v", err)
			}
		})
	}
	for _, kind := range []string{protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite, protocol.OpClaudeTurnStart} {
		t.Run(kind+"/workspace_before_configuration", func(t *testing.T) {
			// No adapters or credential state: an invalid workspace must be rejected
			// before any native configuration, environment or collaboration setup.
			client := &Client{config: Config{ScanRoots: []string{t.TempDir()}}}
			raw, _ := json.Marshal(map[string]string{"workspace": t.TempDir(), "thread_id": "thread"})
			err := client.execute(context.Background(), &protocol.ControlEnvelope{Kind: kind, PayloadJSON: raw})
			if err == nil || err.Error() != "workspace path is outside the configured scan roots" {
				t.Fatalf("workspace check moved: %v", err)
			}
		})
	}
}
