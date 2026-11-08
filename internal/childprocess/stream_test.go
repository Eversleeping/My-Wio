//go:build !windows

package childprocess

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestStreamReachesEOFWhenDescendantKeepsStdoutOpen(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo result; sleep 30 & exit 0")
	cmd.WaitDelay = 200 * time.Millisecond
	stream, err := StartStream(cmd)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var output []byte
	go func() {
		output, _ = io.ReadAll(stream)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stdout did not reach EOF while a descendant held it open")
	}
	if strings.TrimSpace(string(output)) != "result" {
		t.Fatalf("output = %q", output)
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait error = %v", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("command was not reaped")
	}
}

func TestStreamWaitAfterEarlyStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "while :; do echo line; done")
	cmd.WaitDelay = time.Second
	stream, err := StartStream(cmd)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	if _, err := stream.Read(buffer); err != nil {
		t.Fatal(err)
	}
	cancel()
	finished := make(chan error, 1)
	go func() { finished <- stream.Wait() }()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("expected the cancelled command to report an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait blocked after the reader stopped")
	}
	if stream.Wait() == nil {
		t.Fatal("repeated Wait lost the result")
	}
}

func TestStreamPropagatesExitStatus(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo partial; exit 4")
	cmd.WaitDelay = time.Second
	stream, err := StartStream(cmd)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(stream)
	var exitErr *exec.ExitError
	if err := stream.Wait(); err == nil || !asExit(err, &exitErr) || exitErr.ExitCode() != 4 {
		t.Fatalf("Wait error = %v", err)
	}
}

func asExit(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	*target = exitErr
	return ok
}
