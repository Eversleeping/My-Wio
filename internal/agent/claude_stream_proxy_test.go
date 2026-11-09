package agent

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestClaudeStreamProxyRoutesOnlyCustomEndpoints(t *testing.T) {
	c := &Client{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Setenv("NO_PROXY", "internal.example")
	t.Setenv("no_proxy", "")
	routed, stop := c.claudeStreamProxy([]string{"DISABLE_AUTOUPDATER=1", "ANTHROPIC_BASE_URL=https://relay.example.com/api"})
	defer stop()
	if !strings.HasPrefix(routed[1], "ANTHROPIC_BASE_URL=http://127.0.0.1:") || routed[0] != "DISABLE_AUTOUPDATER=1" {
		t.Fatalf("custom endpoint not routed: %v", routed)
	}
	if joined := strings.Join(routed, "|"); !strings.Contains(joined, "NO_PROXY=internal.example,127.0.0.1,localhost") || !strings.Contains(joined, "no_proxy=127.0.0.1,localhost") {
		t.Fatalf("loopback not exempted from proxies: %v", routed)
	}
	for _, environment := range [][]string{{"ANTHROPIC_BASE_URL=https://api.anthropic.com"}, {"ANTHROPIC_BASE_URL="}, {"DISABLE_AUTOUPDATER=1"}} {
		kept, stop := c.claudeStreamProxy(environment)
		stop()
		if strings.Join(kept, "|") != strings.Join(environment, "|") {
			t.Fatalf("environment changed: %v -> %v", environment, kept)
		}
	}
	t.Setenv("WIO_CLAUDE_STREAM_PROXY", "off")
	kept, stop := c.claudeStreamProxy([]string{"ANTHROPIC_BASE_URL=https://relay.example.com"})
	stop()
	if kept[0] != "ANTHROPIC_BASE_URL=https://relay.example.com" {
		t.Fatalf("opt-out ignored: %v", kept)
	}
}
