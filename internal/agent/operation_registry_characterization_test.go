package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestDispatchPreservesPrefixFallbackAndUnknownErrors(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"unknown.operation", "deploy.unknown", "codex.goalish", "deploy.containerish"} {
		client := outboundTestClient(1)
		restart, data, refresh, err := client.dispatchOperation(ctx, &protocol.ControlEnvelope{Kind: kind, PayloadJSON: json.RawMessage(`{`)})
		if err == nil || err.Error() != fmt.Sprintf("unsupported operation kind %q", kind) || restart != "" || data != nil || refresh {
			t.Fatalf("unknown kind %s changed: %s %s %v %v", kind, restart, data, refresh, err)
		}
	}
	for _, kind := range []string{"codex.goal.future", "deploy.container.future"} {
		client := outboundTestClient(1)
		_, _, _, err := client.dispatchOperation(ctx, &protocol.ControlEnvelope{Kind: kind, PayloadJSON: json.RawMessage(`{`)})
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("prefix %s bypassed native decoding: %v", kind, err)
		}
	}
	client := NewClient(Config{StateDir: t.TempDir(), CodexPath: filepath.Join(t.TempDir(), "missing-codex")}, outboundTestClient(1).log)
	defer client.codex.Close()
	_, _, _, err := client.dispatchOperation(ctx, &protocol.ControlEnvelope{Kind: "codex.goal.future", PayloadJSON: json.RawMessage(`{}`)})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unknown goal skipped native startup: %v", err)
	}
	_, data, _, err := client.dispatchOperation(ctx, &protocol.ControlEnvelope{Kind: "deploy.container.future", PayloadJSON: json.RawMessage(`{}`)})
	if err == nil || err.Error() != "container action target is required" || string(data) != `{"target_id":"","action":"","state":""}` {
		t.Fatalf("container prefix changed validation/result: %s %v", data, err)
	}
}

func TestDispatchKeepsPermissiveDecodingAndInventoryIgnoresPayload(t *testing.T) {
	client := outboundTestClient(2)
	_, _, _, err := client.dispatchOperation(context.Background(), &protocol.ControlEnvelope{Kind: protocol.OpCodexCredentialsConfigure, PayloadJSON: json.RawMessage(`{"scope":"git","future_option":true}`)})
	if err == nil || err.Error() != "credential update scope mismatch" {
		t.Fatalf("unknown field changed credential validation: %v", err)
	}
	client.config.ScanRoots = []string{t.TempDir()}
	restart, data, refresh, err := client.dispatchOperation(context.Background(), &protocol.ControlEnvelope{Kind: protocol.OpInventoryScan, PayloadJSON: json.RawMessage(`{`)})
	if err != nil || restart != "" || data != nil || refresh {
		t.Fatalf("inventory began decoding payload: %s %s %v %v", restart, data, refresh, err)
	}
	if receiveAgentEnvelope(t, client.outbound).Kind != protocol.AgentKindInventory {
		t.Fatal("inventory did not publish")
	}
}

func TestLegacyExecuteRejectsSpecializedDispatchKinds(t *testing.T) {
	client := outboundTestClient(1)
	for _, kind := range []string{protocol.OpAgentUpdate, protocol.OpWorkspaceFiles, protocol.OpClaudeTurnInterrupt, "codex.goal.future", "deploy.container.future"} {
		err := client.execute(context.Background(), &protocol.ControlEnvelope{Kind: kind, PayloadJSON: json.RawMessage(`{`)})
		if err == nil || err.Error() != fmt.Sprintf("unsupported operation kind %q", kind) {
			t.Fatalf("legacy execute changed %s: %v", kind, err)
		}
	}
}
