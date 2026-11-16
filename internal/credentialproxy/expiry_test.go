package credentialproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeCredentialExpiryRevokesTheLease(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "synthetic inference")
	}))
	defer upstream.Close()
	c := Config{Provider: "codex", Upstream: upstream.URL, Credentials: []Credential{{Header: "Authorization", Value: "Bearer " + syntheticSecret}}, ExpiresAt: time.Now().Add(100 * time.Millisecond)}
	b, err := Start(c)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	response, err := http.DefaultClient.Do(requestFor(b, "POST", "/responses"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case <-b.lifetime.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("expired authentication was not revoked")
	}
	response, err = http.DefaultClient.Do(requestFor(b, "POST", "/responses"))
	if err == nil {
		response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatal("expired lease still accepted requests")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("expired lease reached its upstream")
	}
	c.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := Start(c); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatal("expired native authentication was accepted")
	}
}
