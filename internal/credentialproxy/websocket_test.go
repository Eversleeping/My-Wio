package credentialproxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func nativeSocketBroker(t *testing.T, upstream string) *Lease {
	t.Helper()
	b, err := Start(Config{Provider: "codex", Native: true, AccountID: "synthetic-account", Upstream: upstream, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stop)
	return b
}

func TestNativeSocketAuthenticationRoutingAndEchoFiltering(t *testing.T) {
	var handshakes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+syntheticSecret || r.Header.Get("ChatGPT-Account-Id") != "synthetic-account" {
			t.Error("native socket lost fixed upstream authentication")
		}
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_, _, err = connection.ReadMessage()
		if err != nil {
			return
		}
		raw := fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q,"nested":{"base64":%q,"escaped":%q},"counter":9007199254740993}`, syntheticSecret, base64.StdEncoding.EncodeToString([]byte(syntheticSecret)), url.QueryEscape(syntheticSecret))
		_ = connection.WriteMessage(websocket.TextMessage, []byte(raw))
		_, _, _ = connection.ReadMessage()
	}))
	defer upstream.Close()
	b := nativeSocketBroker(t, upstream.URL)
	other := nativeSocketBroker(t, upstream.URL)
	for _, entry := range []struct{ path, token, origin string }{
		{"/responses", other.token, ""},
		{"/account", b.token, ""},
		{"/responses", b.token, "https://foreign.example.test"},
	} {
		headers := http.Header{"Authorization": []string{"Bearer " + entry.token}}
		if entry.origin != "" {
			headers.Set("Origin", entry.origin)
		}
		connection, response, err := websocket.DefaultDialer.Dial("ws://"+b.address+entry.path, headers)
		if connection != nil {
			connection.Close()
		}
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if err == nil {
			t.Error("unauthorized native socket connected")
		}
	}
	if handshakes.Load() != 0 {
		t.Fatal("rejected socket reached the fixed upstream")
	}
	connection, _, err := websocket.DefaultDialer.Dial("ws://"+b.address+"/responses", http.Header{"Authorization": []string{"Bearer " + b.token}, "ChatGPT-Account-Id": []string{"forged-account"}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	_, message, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(message, []byte(syntheticSecret)) || bytes.Contains(message, []byte(base64.StdEncoding.EncodeToString([]byte(syntheticSecret)))) || !bytes.Contains(message, []byte("[redacted]")) {
		t.Fatal("recoverable native credential reached the client")
	}
	var valid any
	if json.Unmarshal(message, &valid) != nil || !bytes.Contains(message, []byte("9007199254740993")) {
		t.Fatal("response filtering changed unrelated JSON values")
	}
}

func TestNativeSocketRedirectNeverReachesAnotherOrigin(t *testing.T) {
	var reached atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer foreign.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", foreign.URL+"/responses")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	b := nativeSocketBroker(t, upstream.URL)
	connection, response, err := websocket.DefaultDialer.Dial("ws://"+b.address+"/responses", http.Header{"Authorization": []string{"Bearer " + b.token}})
	if connection != nil {
		connection.Close()
	}
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err == nil || reached.Load() != 0 {
		t.Fatal("native socket followed an upstream redirect")
	}
}

func TestNativeSocketStopRevokesActiveAndFutureConnections(t *testing.T) {
	received := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		if _, _, err = connection.ReadMessage(); err == nil {
			received <- struct{}{}
		}
		_, _, _ = connection.ReadMessage()
	}))
	defer upstream.Close()
	b := nativeSocketBroker(t, upstream.URL)
	connection, _, err := websocket.DefaultDialer.Dial("ws://"+b.address+"/responses", http.Header{"Authorization": []string{"Bearer " + b.token}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","input":[]}`))
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("native request not forwarded")
	}
	b.stop()
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = connection.ReadMessage(); err == nil {
		t.Fatal("native connection survived revocation")
	}
	next, response, err := websocket.DefaultDialer.Dial("ws://"+b.address+"/responses", http.Header{"Authorization": []string{"Bearer " + b.token}})
	if next != nil {
		next.Close()
	}
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("revoked native lease created another connection")
	}
}

func TestNativeSocketCannotForwardManagementMessages(t *testing.T) {
	var forwarded atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		if _, _, err = connection.ReadMessage(); err == nil {
			forwarded.Add(1)
		}
	}))
	defer upstream.Close()
	b := nativeSocketBroker(t, upstream.URL)
	connection, _, err := websocket.DefaultDialer.Dial("ws://"+b.address+"/responses", http.Header{"Authorization": []string{"Bearer " + b.token}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"account.delete","target":"other-account"}`))
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = connection.ReadMessage(); err == nil {
		t.Fatal("unsupported native message kept the connection alive")
	}
	if forwarded.Load() != 0 {
		t.Fatal("management message reached the inference endpoint")
	}
	_, err = b.redactSocketMessage([]byte(`{"type":"x"}{"unexpected":"` + strings.Repeat("x", 8) + `"}`))
	if err == nil {
		t.Fatal("ambiguous response JSON accepted")
	}
}
