package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/protocol/wirecontract"
)

func TestOperationHandlersCoverWireRegistry(t *testing.T) {
	kinds := make([]string, 0, len(operationHandlers))
	for kind, handler := range operationHandlers {
		if handler.run == nil {
			t.Errorf("missing handler for %s", kind)
		}
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	if !reflect.DeepEqual(kinds, wirecontract.OperationKinds()) {
		t.Fatalf("handler/contract mismatch\nhandlers %v\ncontracts %v", kinds, wirecontract.OperationKinds())
	}
	prefixes := make([]string, 0, len(operationPrefixHandlers))
	for _, handler := range operationPrefixHandlers {
		if handler.run == nil {
			t.Errorf("missing prefix handler %s", handler.prefix)
		}
		for _, previous := range prefixes {
			if strings.HasPrefix(handler.prefix, previous) || strings.HasPrefix(previous, handler.prefix) {
				t.Errorf("overlapping prefixes %s and %s", previous, handler.prefix)
			}
		}
		prefixes = append(prefixes, handler.prefix)
	}
	if !reflect.DeepEqual(prefixes, []string{protocol.OpCodexGoalPrefix, protocol.OpDeployContainerPrefix}) {
		t.Fatalf("changed legacy prefix families: %v", prefixes)
	}
}

func TestEveryRegisteredCommandRejectsMalformedPayloadBeforeSideEffects(t *testing.T) {
	for _, kind := range wirecontract.OperationKinds() {
		if kind == protocol.OpInventoryScan {
			continue
		} // Inventory intentionally ignores its payload.
		t.Run(kind, func(t *testing.T) {
			client := outboundTestClient(1)
			restart, data, refresh, err := client.dispatchOperation(context.Background(), &protocol.ControlEnvelope{Kind: kind, PayloadJSON: json.RawMessage(`{`)})
			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) || restart != "" || data != nil || refresh {
				t.Fatalf("%s reached side effects before decoding: %s %s %v %v", kind, restart, data, refresh, err)
			}
		})
	}
}

func TestOperationRegistryPreservesLegacyExecuteKinds(t *testing.T) {
	legacy := map[string]bool{}
	for _, kind := range []string{
		protocol.OpInventoryScan, protocol.OpGitImport, protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite,
		protocol.OpClaudeTurnStart, protocol.OpClaudeQuestionAnswer, protocol.OpGeminiQuestionAnswer,
		protocol.OpGeminiTurnInterrupt, protocol.OpGeminiTurnSteer, protocol.OpCodexTurnStart, protocol.OpCodexTurnRewrite,
		protocol.OpCodexTurnSteer, protocol.OpCodexTurnInterrupt, protocol.OpCodexApproval,
		protocol.OpGeminiCredentialsConfigure, protocol.OpClaudeCredentialsConfigure, protocol.OpCredentialsConfigure,
		protocol.OpCodexCredentialsConfigure, protocol.OpGitCredentialsConfigure, protocol.OpCodexUpdate,
		protocol.OpCLIInstall, protocol.OpDeployStart, protocol.OpDeployRollback,
	} {
		legacy[kind] = true
	}
	for kind, handler := range operationHandlers {
		if (handler.execute != nil) != legacy[kind] {
			t.Errorf("legacy execute registration changed for %s", kind)
		}
	}
}
