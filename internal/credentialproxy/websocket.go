package credentialproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

func (b *Lease) redactSocketMessage(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload any
	if decoder.Decode(&payload) != nil {
		return nil, errors.New("unsupported response message")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("ambiguous response message")
	}
	var clean func(any) any
	clean = func(v any) any {
		switch item := v.(type) {
		case string:
			return b.redact(item)
		case map[string]any:
			result := make(map[string]any, len(item))
			for key, value := range item {
				result[b.redact(key)] = clean(value)
			}
			return result
		case []any:
			for i, value := range item {
				item[i] = clean(value)
			}
			return item
		}
		return v
	}
	return json.Marshal(clean(payload))
}

func (b *Lease) serveSocket(ctx context.Context, w http.ResponseWriter, req *http.Request, target *url.URL, headers http.Header) {
	if origin := req.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != b.address || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			http.Error(w, "unsupported socket origin", http.StatusForbidden)
			return
		}
	}
	endpoint := *target
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	dialer := websocket.Dialer{Proxy: b.transport.Proxy, NetDialContext: b.transport.DialContext, TLSClientConfig: b.transport.TLSClientConfig, HandshakeTimeout: 30 * time.Second}
	upstream, response, err := dialer.DialContext(ctx, endpoint.String(), headers)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		http.Error(w, "native inference socket unavailable", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	downstream, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer downstream.Close()
	upstream.SetReadLimit(32 << 20)
	downstream.SetReadLimit(32 << 20)
	release := context.AfterFunc(ctx, func() { upstream.Close(); downstream.Close() })
	defer release()
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			kind, message, err := downstream.ReadMessage()
			if err != nil || kind != websocket.TextMessage || b.closed.Load() {
				return
			}
			var frame struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(message, &frame) != nil || (frame.Type != "response.create" && frame.Type != "response.cancel") {
				return
			}
			if upstream.WriteMessage(websocket.TextMessage, message) != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			kind, message, err := upstream.ReadMessage()
			if err != nil || kind != websocket.TextMessage || b.closed.Load() {
				return
			}
			message, err = b.redactSocketMessage(message)
			if err != nil || downstream.WriteMessage(websocket.TextMessage, message) != nil {
				return
			}
		}
	}()
	<-done
	upstream.Close()
	downstream.Close()
	<-done
}
