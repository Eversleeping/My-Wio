//go:build !windows

package childprocess

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOutputReturnsWhenDescendantKeepsPipeOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := CommandContext(ctx, "sh", "-c", "echo ready; sleep 30 & exit 0")
	cmd.WaitDelay = 200 * time.Millisecond
	started := time.Now()
	output, err := Output(cmd)
	if err != nil {
		t.Fatalf("Output error = %v", err)
	}
	if strings.TrimSpace(string(output)) != "ready" {
		t.Fatalf("Output = %q", output)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("Output waited %s for a descendant", elapsed)
	}
}

func TestCombinedOutputKeepsCommandFailure(t *testing.T) {
	output, err := CombinedOutput(CommandContext(context.Background(), "sh", "-c", "echo failed >&2; exit 3"))
	if err == nil || strings.TrimSpace(string(output)) != "failed" {
		t.Fatalf("CombinedOutput = %q, %v", output, err)
	}
}
