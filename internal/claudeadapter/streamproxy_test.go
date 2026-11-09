package claudeadapter

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const proxiedStream = "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"event: message_stop\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// A relay that keeps the stream open with pings after message_stop must not
// hold the CLI: the proxy ends the response right after message_stop.
func TestStreamProxyEndsResponseAfterMessageStop(t *testing.T) {
	var seen http.Header
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, path = r.Header.Clone(), r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && r.URL.Query().Get("gzip") == "1" {
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			_, _ = io.WriteString(zw, proxiedStream)
			_ = zw.Flush()
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			_ = zw.Close()
			return
		}
		_, _ = io.WriteString(w, proxiedStream)
		w.(http.Flusher).Flush()
		for i := 0; i < 10; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(500 * time.Millisecond):
				_, _ = io.WriteString(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer upstream.Close()
	base, stop, err := StartStreamProxy(upstream.URL + "/relay")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, query := range []string{"", "?gzip=1"} {
		request, _ := http.NewRequest(http.MethodPost, base+"/v1/messages"+query, strings.NewReader(`{"stream":true}`))
		request.Header.Set("x-api-key", "client-key")
		request.Header.Set("anthropic-beta", "test-beta")
		request.Header.Set("Accept-Encoding", "br")
		started := time.Now()
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != proxiedStream {
			t.Fatalf("query %q: body=%q err=%v", query, body, err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("query %q: response stayed open for %s after message_stop", query, elapsed)
		}
		if path != "/relay/v1/messages" || seen.Get("x-api-key") != "client-key" || seen.Get("anthropic-beta") != "test-beta" || strings.Contains(seen.Get("Accept-Encoding"), "br") {
			t.Fatalf("forwarded request: path=%s headers=%v", path, seen)
		}
	}
}

func TestStreamProxyPassesOtherResponsesThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error"}}`)
	}))
	defer upstream.Close()
	base, stop, err := StartStreamProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	response, err := http.Get(base + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), "rate_limit_error") {
		t.Fatalf("passthrough: %d %s", response.StatusCode, body)
	}
	if _, _, err = StartStreamProxy("ftp://example.com"); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
}

func TestSSEStopReaderIgnoresLookalikes(t *testing.T) {
	stream := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"message_stop\"}}\n\n" +
		"event: ping\r\ndata: {\"type\":\"ping\"}\r\n\r\n" +
		"data: {\"type\": \"message_stop\"}\n\n" +
		"event: ping\ndata: {}\n\n"
	reader := &sseStopReader{body: io.NopCloser(strings.NewReader(stream))}
	got, err := io.ReadAll(reader)
	want := stream[:strings.Index(stream, "event: ping\ndata: {}")]
	if err != nil || string(got) != want {
		t.Fatalf("got %q want %q err %v", got, want, err)
	}
}
