package codexadapter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestMessageForkTrimsLegacyContextOnlyOnNewThread(t *testing.T) {
	a := &Adapter{threads: map[string]string{}}
	var methods []string
	result, err := a.forkThread(context.Background(), protocol.ForkThreadCommand{CodexThread: "source", TargetThreadID: "new-wio", LastTurnID: "keep"}, func(_ context.Context, method string, raw any) (json.RawMessage, error) {
		methods = append(methods, method)
		params := raw.(map[string]any)
		switch method {
		case "thread/fork":
			if params["threadId"] != "source" || params["lastTurnId"] != "keep" {
				t.Fatalf("bad fork params: %#v", params)
			}
			return json.RawMessage(`{"thread":{"id":"new-native","turns":[{"id":"keep","status":"completed"},{"id":"later"},{"id":"newest"}]}}`), nil
		case "thread/rollback":
			if params["threadId"] != "new-native" || params["numTurns"] != uint32(2) {
				t.Fatalf("bad rollback: %#v", params)
			}
			return json.RawMessage(`{}`), nil
		default:
			t.Fatalf("unexpected RPC %s", method)
			return nil, nil
		}
	})
	if err != nil || result.LastTurnID != "keep" || result.CodexThread != "new-native" || a.threads["new-native"] != "new-wio" {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
	if !reflect.DeepEqual(methods, []string{"thread/fork", "thread/rollback"}) {
		t.Fatal(methods)
	}
}

func TestMessageForkVerifiesPaginatedHistory(t *testing.T) {
	for _, trimmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "native-last-turn-supported", true: "legacy-paginated-fallback"}[trimmed], func(t *testing.T) {
			a := &Adapter{threads: map[string]string{}}
			var rollback bool
			result, err := a.forkThread(context.Background(), protocol.ForkThreadCommand{CodexThread: "source", TargetThreadID: "wio", LastTurnID: "keep"}, func(_ context.Context, method string, raw any) (json.RawMessage, error) {
				params := raw.(map[string]any)
				switch method {
				case "thread/fork":
					return json.RawMessage(`{"thread":{"id":"new-native","turns":[]}}`), nil
				case "thread/turns/list":
					if params["threadId"] != "new-native" {
						t.Fatal(params)
					}
					if trimmed && params["cursor"] == nil {
						return json.RawMessage(`{"data":[{"id":"later"}],"nextCursor":"older"}`), nil
					}
					return json.RawMessage(`{"data":[{"id":"keep","status":"completed"}],"nextCursor":null}`), nil
				case "thread/rollback":
					rollback = true
					if params["threadId"] != "new-native" || params["numTurns"] != uint32(1) {
						t.Fatal(params)
					}
					return json.RawMessage(`{}`), nil
				default:
					t.Fatalf("unexpected %s", method)
					return nil, nil
				}
			})
			if err != nil || result.LastTurnID != "keep" || rollback != trimmed {
				t.Fatalf("result=%#v rollback=%v err=%v", result, rollback, err)
			}
		})
	}
}

func TestMessageForkFailureDoesNotExposeUntrimmedChat(t *testing.T) {
	for _, turns := range []string{`[{"id":"different"}]`, `[{"id":"keep","status":"inProgress"}]`, `[{"id":"keep"},{"id":"later"}]`} {
		a := &Adapter{threads: map[string]string{}}
		archived := false
		result, err := a.forkThread(context.Background(), protocol.ForkThreadCommand{CodexThread: "source", TargetThreadID: "wio", LastTurnID: "keep"}, func(_ context.Context, method string, raw any) (json.RawMessage, error) {
			switch method {
			case "thread/fork":
				return json.RawMessage(`{"thread":{"id":"new-native","turns":` + turns + `}}`), nil
			case "thread/rollback":
				return nil, errors.New("RPC timeout")
			case "thread/archive":
				if raw.(map[string]any)["threadId"] != "new-native" {
					t.Fatal(raw)
				}
				archived = true
				return json.RawMessage(`{}`), nil
			default:
				t.Fatalf("must not retry a possibly completed mutation: %s", method)
				return nil, nil
			}
		})
		if err == nil || result.CodexThread != "" || len(a.threads) != 0 || !archived {
			t.Fatalf("exposed invalid fork: %#v %v", result, err)
		}
	}
}

func TestMessageForkReadsHistoryOnlyForExplicitUnsupportedPagination(t *testing.T) {
	a := &Adapter{threads: map[string]string{}}
	_, err := a.forkThread(context.Background(), protocol.ForkThreadCommand{CodexThread: "source", TargetThreadID: "wio", LastTurnID: "keep"}, func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		switch method {
		case "thread/fork":
			return json.RawMessage(`{"thread":{"id":"new-native"}}`), nil
		case "thread/turns/list":
			return nil, &rpcRequestError{Code: -32601, Message: "Method not found"}
		case "thread/read":
			return json.RawMessage(`{"thread":{"turns":[{"id":"keep"}]}}`), nil
		default:
			t.Fatalf("unexpected %s", method)
			return nil, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}
