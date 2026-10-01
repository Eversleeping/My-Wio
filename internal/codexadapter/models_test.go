package codexadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelsListPaginatesFiltersAndUsesModelSlug(t *testing.T) {
	calls := 0
	result, err := (&Adapter{}).codexOperation(context.Background(), "codex.models.list", json.RawMessage(`{"codex_version":"0.159.3"}`), func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if method != "model/list" {
			t.Fatalf("unexpected method %s", method)
		}
		p := params.(map[string]any)
		if p["includeHidden"] != false {
			t.Fatal("hidden models were requested")
		}
		calls++
		if calls == 1 {
			return json.RawMessage(`{"data":[{"id":"picker-id","model":"new-model","displayName":"New Model","secret":"private"},{"model":"hidden","hidden":true}],"nextCursor":"page-2"}`), nil
		}
		if p["cursor"] != "page-2" {
			t.Fatal("pagination cursor missing")
		}
		return json.RawMessage(`{"data":[{"model":"new-model"},{"id":"another-model"}],"nextCursor":null}`), nil
	})
	if err != nil || !result.Supported || result.CodexVersion != "0.159.3" {
		t.Fatalf("unexpected result: %+v %v", result, err)
	}
	var models []codexModel
	if err := json.Unmarshal(result.Data, &models); err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Model != "new-model" || models[1].Model != "another-model" || calls != 2 {
		t.Fatalf("unexpected models: %+v", models)
	}
	if strings.Contains(string(result.Data), "private") {
		t.Fatal("unrecognized metadata leaked")
	}
}

func TestModelsListUnsupportedAndInvalidPagination(t *testing.T) {
	for _, test := range []struct {
		name      string
		response  json.RawMessage
		rpcError  error
		supported bool
		wantError bool
	}{
		{name: "unsupported", rpcError: &rpcRequestError{Code: -32601, Message: "method not found"}},
		{name: "empty", response: json.RawMessage(`{"data":[]}`), supported: true},
		{name: "missing data", response: json.RawMessage(`{}`), wantError: true},
		{name: "repeated cursor", response: json.RawMessage(`{"data":[],"nextCursor":"loop"}`), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := codexModelsOperation(context.Background(), "0.159.3", func(context.Context, string, any) (json.RawMessage, error) { return test.response, test.rpcError })
			if (err != nil) != test.wantError || result.Supported != test.supported {
				t.Fatalf("unexpected result: %+v %v", result, err)
			}
		})
	}
}
