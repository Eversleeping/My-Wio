package credentialproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlowUploadDeadlineReleasesAdmissionAndCancelsUpstream(t *testing.T) {
	var active atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		_, err := io.Copy(io.Discard, r.Body)
		if err == nil {
			_, _ = io.WriteString(w, "safe inference")
		}
	}))
	t.Cleanup(upstream.Close)
	b, err := Start(Config{Provider: "codex", Upstream: upstream.URL, Credentials: []Credential{{"Authorization", "Bearer " + syntheticSecret}}, RequestReadTimeoutMS: 300})
	if err != nil {
		t.Fatal("cannot start deadline fixture")
	}
	t.Cleanup(b.stop)
	connections := make([]net.Conn, 0, 16)
	t.Cleanup(func() {
		for _, c := range connections {
			_ = c.Close()
		}
	})
	for i := 0; i < 16; i++ {
		c, err := net.DialTimeout("tcp", b.address, time.Second)
		if err != nil {
			t.Fatal("cannot start bounded slow upload")
		}
		connections = append(connections, c)
		_, err = fmt.Fprintf(c, "POST /responses HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Length: 2\r\n\r\n{", b.address, b.token)
		if err != nil {
			t.Fatal("cannot send bounded slow upload")
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(b.slots) != 0 || active.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("expired uploads retained admission or upstream work")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Wait beyond the configured input deadline even if handlers were not
	// scheduled before the first observation.
	time.Sleep(400 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := http.DefaultClient.Do(requestFor(b, "POST", "/responses").WithContext(ctx))
	if err != nil {
		t.Fatal("inference did not recover after expired uploads")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(body) != "safe inference" || len(b.slots) != 0 || active.Load() != 0 {
		t.Fatal("slow uploads prevented ordinary inference recovery")
	}
}

func TestUnknownLengthOversizedUploadHasBoundedForwarding(t *testing.T) {
	const limit = 32 << 20
	sizes := make(chan int64, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		sizes <- n
	}))
	t.Cleanup(upstream.Close)
	b := reviewBroker(t, "codex", upstream.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+b.address+"/responses", io.LimitReader(reviewZeros{}, limit+1))
	req.Header.Set("Authorization", "Bearer "+b.token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("streamed oversized upload had no bounded response")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 502 || bytes.Contains(body, []byte(syntheticSecret)) {
		t.Fatal("streamed oversized input did not fail safely")
	}
	select {
	case n := <-sizes:
		if n > limit {
			t.Fatal("streamed input exceeded the forwarding bound")
		}
	case <-ctx.Done():
		t.Fatal("streamed oversized input retained upstream work")
	}
}
