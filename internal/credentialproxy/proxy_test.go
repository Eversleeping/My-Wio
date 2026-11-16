package credentialproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticSecret = "p504-upstream-only-synthetic-secret"

func requestFor(b *Lease, method, path string) *http.Request {
	req, _ := http.NewRequest(method, "http://"+b.address+path, strings.NewReader(`{"synthetic":true}`))
	switch b.config.Provider {
	case "codex":
		req.Header.Set("Authorization", "Bearer "+b.token)
	case "claude":
		req.Header.Set("x-api-key", b.token)
	case "gemini":
		req.Header.Set("x-goog-api-key", b.token)
	}
	return req
}

func TestFixedUpstreamAuthenticationAndResponseRedaction(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			var reached atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+syntheticSecret || r.Header.Get("x-api-key") != "" || r.Header.Get("x-goog-api-key") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Forwarded") != "" || r.URL.Query().Get("key") != "" || r.URL.Query().Get("token") != "" {
					t.Error("upstream authentication or sanitization failed")
				}
				reached.Store(true)
				w.Header().Set("x-request-id", syntheticSecret)
				w.Header().Set("Set-Cookie", syntheticSecret)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"echo\":\"")
				w.(http.Flusher).Flush()
				io.WriteString(w, syntheticSecret[:7])
				w.(http.Flusher).Flush()
				io.WriteString(w, syntheticSecret[7:]+"\"}\n\nevent: message_stop\n\n")
			}))
			defer upstream.Close()
			b, err := Start(Config{Provider: provider, Upstream: upstream.URL, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
			if err != nil {
				t.Fatal(err)
			}
			defer b.stop()
			path := map[string]string{"codex": "/v1/responses", "claude": "/v1/messages", "gemini": "/v1beta/models/gemini-test:generateContent"}[provider]
			query := "?key=untrusted&token=untrusted"
			if provider == "gemini" {
				query += "&alt=sse"
			}
			req := requestFor(b, "POST", path+query)
			req.Header.Set("Cookie", "untrusted")
			req.Header.Set("Forwarded", "host=untrusted")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != 200 || !reached.Load() || bytes.Contains(body, []byte(syntheticSecret)) || !bytes.Contains(body, []byte("[redacted]")) || response.Header.Get("x-request-id") != "[redacted]" || response.Header.Get("Set-Cookie") != "" {
				t.Fatal("protected response was exposed or inference did not run")
			}
		})
	}
}

func TestUnauthorizedForeignHostAndUnsupportedRoutesNeverReachUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	b, _ := Start(Config{Provider: "codex", Upstream: upstream.URL, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	defer b.stop()
	for _, kind := range []string{"no-auth", "foreign-host", "connect", "absolute", "encoded", "traversal", "debug", "upgrade"} {
		req := requestFor(b, "POST", "/v1/responses")
		switch kind {
		case "no-auth":
			req.Header.Del("Authorization")
		case "foreign-host":
			req.Host = "attacker.invalid"
		case "connect":
			req.Method = "CONNECT"
		case "absolute":
			req.URL, _ = url.Parse("http://attacker.invalid/v1/responses")
		case "encoded":
			req.URL.RawPath = "/v1/%72esponses"
		case "traversal":
			req.URL.Path = "/v1/../debug"
		case "debug":
			req.URL.Path = "/debug"
		case "upgrade":
			req.Header.Set("Upgrade", "websocket")
		}
		req.RemoteAddr = "127.0.0.1:12345"
		req.Host = map[bool]string{true: "attacker.invalid", false: b.address}[kind == "foreign-host"]
		recorder := httptest.NewRecorder()
		b.ServeHTTP(recorder, req)
		if recorder.Code < 400 {
			t.Fatal("request bypassed route guard: " + kind)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("an untrusted route reached the fixed upstream")
	}
}

func TestRedirectCannotForwardCredentialToAnotherOrigin(t *testing.T) {
	var escaped atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Store(true) }))
	defer other.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.URL+"/?key="+syntheticSecret)
		w.WriteHeader(307)
	}))
	defer upstream.Close()
	b, _ := Start(Config{Provider: "codex", Upstream: upstream.URL, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	defer b.stop()
	response, err := http.DefaultClient.Do(requestFor(b, "POST", "/responses"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 502 || escaped.Load() || response.Header.Get("Location") != "" || bytes.Contains(body, []byte(syntheticSecret)) {
		t.Fatal("redirect escaped the fixed upstream")
	}
}

func TestGzipBodyIsRedactedAfterDecompression(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		zip := gzip.NewWriter(w)
		_, _ = zip.Write([]byte(`{"echo":"` + syntheticSecret + `"}`))
		_ = zip.Close()
	}))
	defer upstream.Close()
	b, _ := Start(Config{Provider: "codex", Upstream: upstream.URL, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	defer b.stop()
	response, err := http.DefaultClient.Do(requestFor(b, "POST", "/responses"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if bytes.Contains(body, []byte(syntheticSecret)) || !bytes.Contains(body, []byte("[redacted]")) {
		t.Fatal("compressed credential echo escaped")
	}
}

func TestStopRevokesPendingAndFutureRequests(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTTP/1 cannot monitor peer closure while an unread request body is
		// owned by the handler. An inference upstream consumes its input.
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)
	b, _ := Start(Config{Provider: "codex", Upstream: upstream.URL, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	request := requestFor(b, "POST", "/responses")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request = request.WithContext(ctx)
	finished := make(chan struct{})
	go func() {
		response, _ := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		close(finished)
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	b.stop()
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("active upstream request survived revocation")
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("client survived revocation")
	}
	recorder := httptest.NewRecorder()
	late := requestFor(b, "POST", "/responses")
	late.RemoteAddr = "127.0.0.1:12345"
	b.ServeHTTP(recorder, late)
	if recorder.Code != 403 {
		t.Fatal("revoked lease accepted a new request")
	}
}

type chunkReader struct{ chunks [][]byte }

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func TestRedactorHandlesEverySplitAndDoesNotHoldCompleteEvents(t *testing.T) {
	pattern := []byte(syntheticSecret)
	for split := 1; split < len(pattern); split++ {
		source := &chunkReader{chunks: [][]byte{append([]byte("data: \""), pattern[:split]...), append(pattern[split:], []byte("\"\n\n")...)}}
		body, err := io.ReadAll(&redactor{source: source, patterns: [][]byte{pattern}})
		if err != nil || bytes.Contains(body, pattern) || string(body) != "data: \"[redacted]\"\n\n" {
			t.Fatal("secret crossed an unredacted chunk boundary")
		}
	}
	encoded := base64.StdEncoding.EncodeToString(pattern)
	proxy, _ := Start(Config{Provider: "codex", Upstream: "http://127.0.0.1:1", Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	defer proxy.stop()
	if proxy.redact(encoded) != "[redacted]" {
		t.Fatal("encoded credential echo escaped")
	}
	source := &chunkReader{chunks: [][]byte{[]byte("event: message_stop\n\n"), []byte("later")}}
	r := &redactor{source: source, patterns: [][]byte{pattern}}
	buffer := make([]byte, 100)
	n, err := r.Read(buffer)
	if err != nil || string(buffer[:n]) != "event: message_stop\n\n" || len(source.chunks) != 1 {
		t.Fatal("a complete SSE event was held for a later read")
	}
}
