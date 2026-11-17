package credentialproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func reviewBroker(t *testing.T, provider, upstream string) *Lease {
	t.Helper()
	b, err := Start(Config{Provider: provider, Upstream: upstream, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}})
	if err != nil {
		t.Fatal("cannot start synthetic Lease")
	}
	t.Cleanup(b.stop)
	return b
}

func TestReviewAuthenticationCannotCrossSessions(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = io.WriteString(w, "synthetic inference")
			}))
			t.Cleanup(upstream.Close)
			first, second := reviewBroker(t, provider, upstream.URL), reviewBroker(t, provider, upstream.URL)
			path := map[string]string{"codex": "/v1/responses", "claude": "/v1/messages", "gemini": "/v1beta/models/synthetic:generateContent"}[provider]
			req := requestFor(first, "POST", path)
			req.URL.Host, req.Host = second.address, second.address
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal("cross-session probe failed to reach Lease")
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 403 || calls.Load() != 0 {
				t.Fatal("a different session's lease reached upstream")
			}
			response, err = http.DefaultClient.Do(requestFor(second, "POST", path))
			if err != nil {
				t.Fatal("valid session failed")
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 || calls.Load() != 1 {
				t.Fatal("valid lease was rejected")
			}
		})
	}
}

func TestReviewFixedTargetWithAdversarialHeadersAndQueries(t *testing.T) {
	var otherCalls atomic.Int32
	var fixedCalls atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherCalls.Add(1) }))
	t.Cleanup(other.Close)
	var forwardedUnknown atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixedCalls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		for _, name := range []string{"Proxy-Authorization", "Cookie", "Forwarded", "X-Forwarded-Host", "X-Original-URL", "OpenAI-Organization", "ChatGPT-Account-Id"} {
			if r.Header.Get(name) != "" {
				t.Error("untrusted authority or routing header reached upstream")
			}
		}
		if r.Header.Get("Authorization") != "Bearer "+syntheticSecret {
			t.Error("request replaced fixed authentication")
		}
		for name := range r.URL.Query() {
			switch strings.ToLower(name) {
			case "key", "api_key", "apikey", "access_token", "token":
				t.Error("credential query reached upstream")
			}
		}
		forwardedUnknown.Store(r.URL.Query().Get("url") == other.URL && r.URL.Query().Get("callback") == "synthetic-callback")
		_, _ = io.WriteString(w, "safe")
	}))
	t.Cleanup(upstream.Close)
	b := reviewBroker(t, "codex", upstream.URL)
	q := url.Values{"key": {"untrusted"}, "KeY": {"untrusted"}, "api_key": {"untrusted"}, "APIKey": {"untrusted"}, "access_token": {"untrusted"}, "TOKEN": {"untrusted"}, "url": {other.URL}, "callback": {"synthetic-callback"}}
	req := requestFor(b, "POST", "/responses?"+q.Encode())
	for _, name := range []string{"Proxy-Authorization", "Cookie", "Forwarded", "X-Forwarded-Host", "X-Original-URL", "OpenAI-Organization", "ChatGPT-Account-Id"} {
		req.Header.Set(name, other.URL)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("synthetic query probe failed")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 400 || otherCalls.Load() != 0 || fixedCalls.Load() != 0 {
		t.Fatal("unsupported query controls reached an upstream")
	}
	req = requestFor(b, "POST", "/responses")
	for _, name := range []string{"Proxy-Authorization", "Cookie", "Forwarded", "X-Forwarded-Host", "X-Original-URL", "OpenAI-Organization", "ChatGPT-Account-Id"} {
		req.Header.Set(name, other.URL)
	}
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("valid inference failed")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || fixedCalls.Load() != 1 || otherCalls.Load() != 0 || forwardedUnknown.Load() {
		t.Fatal("valid inference escaped the fixed target or retained query controls")
	}
}

func TestAttackMultipleContentEncodingsCannotExposeRecoverableSecret(t *testing.T) {
	for _, kind := range []string{"identity-then-gzip", "two-gzip-layers"} {
		t.Run(kind, func(t *testing.T) {
			var compressed bytes.Buffer
			zip := gzip.NewWriter(&compressed)
			_, _ = io.WriteString(zip, `{"echo":"`+syntheticSecret+`"}`)
			_ = zip.Close()
			payload := append([]byte(nil), compressed.Bytes()...)
			if kind == "two-gzip-layers" {
				compressed.Reset()
				zip = gzip.NewWriter(&compressed)
				_, _ = zip.Write(payload)
				_ = zip.Close()
				payload = append([]byte(nil), compressed.Bytes()...)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				if kind == "identity-then-gzip" {
					w.Header().Add("Content-Encoding", "identity")
				} else {
					w.Header().Add("Content-Encoding", "gzip")
				}
				w.Header().Add("Content-Encoding", "gzip")
				_, _ = w.Write(payload)
			}))
			t.Cleanup(upstream.Close)
			b := reviewBroker(t, "codex", upstream.URL)
			response, err := http.DefaultClient.Do(requestFor(b, "POST", "/responses"))
			if err != nil {
				t.Fatal("synthetic encoding probe failed")
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode == 502 {
				return
			}
			for layer := 0; layer < 3; layer++ {
				if bytes.Contains(body, []byte(syntheticSecret)) {
					t.Fatal("multiple encoding headers exposed a recoverable upstream credential")
				}
				unzip, unzipErr := gzip.NewReader(bytes.NewReader(body))
				if unzipErr != nil {
					break
				}
				body, _ = io.ReadAll(unzip)
				_ = unzip.Close()
			}
		})
	}
}

func TestReviewCanonicalEchoRepresentationsAcrossSplits(t *testing.T) {
	secret := `synthetic-credential/+"\\with-space tail`
	b, err := Start(Config{Provider: "codex", Upstream: "http://127.0.0.1:1", Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + secret}}})
	if err != nil {
		t.Fatal("cannot configure synthetic echo patterns")
	}
	t.Cleanup(b.stop)
	escaped, _ := json.Marshal(secret)
	representations := []string{secret, url.QueryEscape(secret), url.PathEscape(secret), string(escaped[1 : len(escaped)-1]), base64.StdEncoding.EncodeToString([]byte(secret))}
	for index, value := range representations {
		for split := 1; split < len(value); split++ {
			source := &chunkReader{chunks: [][]byte{[]byte("data: " + value[:split]), []byte(value[split:] + "\n\n")}}
			got, readErr := io.ReadAll(&redactor{source: source, patterns: b.patterns})
			if readErr != nil || string(got) != "data: [redacted]\n\n" {
				t.Fatalf("canonical representation %d was not redacted across split %d", index, split)
			}
		}
	}
}

func TestReviewStopRevokesAlreadyStreamingResponse(t *testing.T) {
	cancelled := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ready\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() { close(release) })
	b := reviewBroker(t, "codex", upstream.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	response, err := http.DefaultClient.Do(requestFor(b, "POST", "/responses").WithContext(ctx))
	if err != nil {
		t.Fatal("stream startup failed")
	}
	defer response.Body.Close()
	first := make([]byte, len("data: ready\n\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal("complete safe SSE event was held")
	}
	b.stop()
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("streaming upstream request survived stop")
	}
	// Cancellation can let the handler return before Server.Close closes the
	// downstream connection. Both EOF and a transport error are valid; neither
	// may leave the stream open or deliver additional data.
	remaining, _ := io.ReadAll(response.Body)
	if ctx.Err() != nil {
		t.Fatal("revoked stream did not close before the client deadline")
	}
	if len(remaining) != 0 {
		t.Fatal("revoked stream delivered additional data")
	}
	conn, err := net.DialTimeout("tcp4", b.address, 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("revoked Lease still accepted connections")
	}
}

func TestReviewSlowUploadsConsumeEntireAdmissionBudget(t *testing.T) {
	started := make(chan struct{}, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "safe")
	}))
	t.Cleanup(upstream.Close)
	b := reviewBroker(t, "codex", upstream.URL)
	var conns []net.Conn
	t.Cleanup(func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	for index := 0; index < 16; index++ {
		conn, err := net.DialTimeout("tcp4", b.address, 500*time.Millisecond)
		if err != nil {
			t.Fatal("cannot start incomplete synthetic upload")
		}
		conns = append(conns, conn)
		_, err = fmt.Fprintf(conn, "POST /responses HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Length: 2\r\nContent-Type: application/json\r\n\r\n{", b.address, b.token)
		if err != nil {
			t.Fatal("cannot send incomplete synthetic upload")
		}
	}
	for index := 0; index < 16; index++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("incomplete upload did not reserve expected slot")
		}
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	<-timer.C
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := http.DefaultClient.Do(requestFor(b, "POST", "/responses").WithContext(ctx))
	if err != nil {
		t.Fatal("ordinary inference probe failed")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 429 {
		t.Fatal("review did not reproduce the current admission-budget behavior")
	}
	t.Log("The sixteen-request admission budget applies while uploads remain incomplete; the separate deadline probe verifies its recovery.")
}

func TestAttackGeminiDotSegmentsCannotReachNonInferenceRoute(t *testing.T) {
	for _, segment := range []string{".", ".."} {
		t.Run(segment, func(t *testing.T) {
			var outsideRoute atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if path.Clean(r.URL.Path) != r.URL.Path {
					outsideRoute.Store(true)
				}
				_, _ = io.WriteString(w, "synthetic normalized endpoint")
			}))
			t.Cleanup(upstream.Close)
			b := reviewBroker(t, "gemini", upstream.URL)
			response, err := http.DefaultClient.Do(requestFor(b, "GET", "/v1beta/models/"+segment))
			if err != nil {
				t.Fatal("synthetic dot-segment probe failed")
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if outsideRoute.Load() {
				t.Fatal("Gemini model route accepted a dot segment that normalizes outside the allowed inference routes")
			}
		})
	}
}

type reviewZeros struct{}

func (reviewZeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestReviewRequestBodyHasBoundedForwarding(t *testing.T) {
	const limit = 32 << 20
	sizes := make(chan int64, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		sizes <- n
		_, _ = io.WriteString(w, "safe")
	}))
	t.Cleanup(upstream.Close)
	b := reviewBroker(t, "codex", upstream.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+b.address+"/responses", io.LimitReader(reviewZeros{}, limit+1))
	req.ContentLength = limit + 1
	req.Header.Set("Authorization", "Bearer "+b.token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("oversized synthetic request did not produce a bounded response")
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 413 || bytes.Contains(body, []byte(syntheticSecret)) {
		t.Fatal("oversized request did not fail with a sanitized response")
	}
	select {
	case <-sizes:
		t.Fatal("known oversized request reached upstream")
	default:
	}
}
