package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/sessiontools"
)

func TestBridgeAuthenticatedProtocolAndLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	bridge, closeBridge, err := Start(ctx, func(_ context.Context, name string, args json.RawMessage) (sessiontools.Reply, error) {
		calls++
		if name != "wio_get_context" || string(args) != "{}" {
			t.Errorf("wrong call: %s %s", name, args)
		}
		return sessiontools.Reply{Status: "succeeded", Data: map[string]string{"bound_session": "source"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeBridge()
	client := &http.Client{Timeout: time.Second}
	post := func(body string, auth bool) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("POST", bridge["url"].(string), bytes.NewBufferString(body))
		if auth {
			req.Header.Set("Authorization", bridge["headers"].(map[string]string)["Authorization"])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	if status, _ := post(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false); status != 401 {
		t.Fatalf("unauthenticated: %d", status)
	}
	for _, version := range []string{"2024-11-05", "2025-03-26", "2025-06-18"} {
		_, raw := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+version+`"}}`, true)
		if !bytes.Contains(raw, []byte(version)) {
			t.Fatalf("initialize: %s", raw)
		}
	}
	_, raw := post(`{"jsonrpc":"2.0","id":"list","method":"tools/list"}`, true)
	var listed struct {
		Result struct {
			Tools []sessiontools.Tool `json:"tools"`
		} `json:"result"`
	}
	if err = json.Unmarshal(raw, &listed); err != nil || len(listed.Result.Tools) != 16 {
		t.Fatalf("tools missing: %s %v", raw, err)
	}
	_, raw = post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"wio_get_context","arguments":{}}}`, true)
	if calls != 1 || !bytes.Contains(raw, []byte("bound_session")) {
		t.Fatalf("call: %d %s", calls, raw)
	}
	if status, _ := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, true); status != 202 {
		t.Fatalf("notification: %d", status)
	}
	_, raw = post(`{"jsonrpc":"2.0","id":3,"method":"unknown"}`, true)
	if !bytes.Contains(raw, []byte("-32601")) {
		t.Fatalf("unknown method: %s", raw)
	}
	if status, _ := post(`{"jsonrpc":"2.0","id":4,"method":"ping"}{}`, true); status != 400 {
		t.Fatalf("trailing JSON: %d", status)
	}
	closeBridge()
	closeBridge()
	if response, err := client.Post(bridge["url"].(string), "application/json", bytes.NewBufferString(`{}`)); err == nil {
		response.Body.Close()
		t.Fatal("closed bridge remains reachable")
	}
}
