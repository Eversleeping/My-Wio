package prerequisite

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureSkipsPackageManagerWhenEverythingIsAvailable(t *testing.T) {
	result, err := ensure(context.Background(), func(_ context.Context, command string, _ ...string) (string, error) {
		switch command {
		case "git", "docker":
			return "available", nil
		default:
			t.Fatal("unexpected package manager command: " + command)
			return "", errors.New("unexpected")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Logs) != 1 || !strings.Contains(result.Logs[0], "already available") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCommandLogTruncatesOutput(t *testing.T) {
	log := commandLog("apt-get update", strings.Repeat("x", 9000))
	if len(log) > 8300 || !strings.HasSuffix(log, "...") {
		t.Fatalf("command log was not bounded: %d", len(log))
	}
}

func TestEnsureInstallsMissingToolsAndChecksDockerAfterStartup(t *testing.T) {
	var commands []string
	installed := map[string]bool{}
	started := false
	_, err := ensureWithManager(context.Background(), func(_ context.Context, command string, args ...string) (string, error) {
		commands = append(commands, command+" "+strings.Join(args, " "))
		switch command {
		case "apt-get":
			if args[0] == "install" {
				installed[args[len(args)-1]] = true
			}
		case "systemctl":
			if !installed["docker.io"] {
				t.Fatal("Docker service was started before installation")
			}
			started = true
		case "git":
			if !installed["git"] {
				return "", errors.New("missing Git")
			}
		case "docker":
			if args[0] == "info" && !started {
				return "", errors.New("Docker service is stopped")
			}
			if args[0] == "compose" && !installed["docker-compose-plugin"] {
				return "", errors.New("missing Compose")
			}
		default:
			t.Fatalf("unexpected command: %s", command)
		}
		return "ready", nil
	}, func() (string, error) { return "apt-get", nil })
	if err != nil {
		t.Fatal(err)
	}
	if !installed["git"] || !installed["docker.io"] || !installed["docker-compose-plugin"] || !started {
		t.Fatalf("not all dependencies installed: %v", commands)
	}
	if !strings.HasSuffix(strings.Join(commands, "\n"), "git --version\ndocker info --format {{.ServerVersion}}\ndocker compose version --short") {
		t.Fatalf("missing final runtime checks: %v", commands)
	}
}

// startTestHelper serves helper requests on a Unix socket with a fake run.
func startTestHelper(t *testing.T, ensure func(context.Context) (Result, error)) (string, *coordinator) {
	t.Helper()
	// Unix socket paths are short; t.TempDir embeds the test name.
	directory, err := os.MkdirTemp("", "wio-prq-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	runs := newCoordinator(ensure)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, listener, runs) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serve returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("helper did not stop")
		}
	})
	return socket, runs
}

type ensureOutcome struct {
	result Result
	err    error
}

func ensureAsync(ctx context.Context, socket string) <-chan ensureOutcome {
	outcome := make(chan ensureOutcome, 1)
	go func() {
		result, err := Ensure(ctx, socket)
		outcome <- ensureOutcome{result: result, err: err}
	}()
	return outcome
}

func receive[T any](t *testing.T, what string, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func currentWaiters(runs *coordinator) int {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if runs.current == nil {
		return 0
	}
	return runs.current.waiters
}

// concurrency records the highest number of runs executing at once.
type concurrency struct {
	mu      sync.Mutex
	active  int
	highest int
}

func (c *concurrency) enter() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active++
	if c.active > c.highest {
		c.highest = c.active
	}
}

func (c *concurrency) exit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
}

func (c *concurrency) maximum() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.highest
}

func TestConcurrentRequestsShareOneRun(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	socket, runs := startTestHelper(t, func(ctx context.Context) (Result, error) {
		calls.Add(1)
		select {
		case <-release:
			return Result{Logs: []string{"installed"}}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	})
	const clients = 3
	outcomes := make([]<-chan ensureOutcome, clients)
	for index := range outcomes {
		outcomes[index] = ensureAsync(context.Background(), socket)
	}
	waitFor(t, "every client to join the run", func() bool { return currentWaiters(runs) == clients })
	close(release)
	for _, outcome := range outcomes {
		got := receive(t, "a shared result", outcome)
		if got.err != nil || strings.Join(got.result.Logs, "\n") != "installed" {
			t.Fatalf("unexpected shared result: %#v %v", got.result, got.err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent requests started %d runs", calls.Load())
	}
}

func TestLaterRequestsStartNewRunsThatNeverOverlap(t *testing.T) {
	var running concurrency
	var calls atomic.Int32
	socket, _ := startTestHelper(t, func(context.Context) (Result, error) {
		running.enter()
		defer running.exit()
		call := calls.Add(1)
		time.Sleep(5 * time.Millisecond)
		return Result{Logs: []string{fmt.Sprintf("run %d", call)}}, nil
	})
	for want := 1; want <= 2; want++ {
		result, err := Ensure(context.Background(), socket)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(result.Logs, "\n"); got != fmt.Sprintf("run %d", want) {
			t.Fatalf("request %d received %q; a finished run must not be reused", want, got)
		}
	}
	var clients sync.WaitGroup
	for index := 0; index < 8; index++ {
		clients.Add(1)
		go func() {
			defer clients.Done()
			if _, err := Ensure(context.Background(), socket); err != nil {
				t.Error(err)
			}
		}()
	}
	clients.Wait()
	if running.maximum() != 1 {
		t.Fatalf("%d runs executed at the same time", running.maximum())
	}
}

func TestDisconnectCancelsRunWithoutOtherWaiters(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	socket, _ := startTestHelper(t, func(ctx context.Context) (Result, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return Result{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	outcome := ensureAsync(ctx, socket)
	receive(t, "the run to start", started)
	cancel()
	if got := receive(t, "the cancelled client", outcome); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled client returned %v", got.err)
	}
	receive(t, "the abandoned run to be cancelled", cancelled)
}

func TestDisconnectKeepsRunForRemainingWaiters(t *testing.T) {
	release := make(chan struct{})
	var cancelled atomic.Bool
	socket, runs := startTestHelper(t, func(ctx context.Context) (Result, error) {
		select {
		case <-release:
			return Result{Logs: []string{"installed"}}, nil
		case <-ctx.Done():
			cancelled.Store(true)
			return Result{}, ctx.Err()
		}
	})
	leavingCtx, leave := context.WithCancel(context.Background())
	leaving := ensureAsync(leavingCtx, socket)
	staying := ensureAsync(context.Background(), socket)
	waitFor(t, "both clients to join the run", func() bool { return currentWaiters(runs) == 2 })
	leave()
	if got := receive(t, "the leaving client", leaving); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("leaving client returned %v", got.err)
	}
	waitFor(t, "the helper to notice the disconnect", func() bool { return currentWaiters(runs) == 1 })
	close(release)
	got := receive(t, "the remaining client", staying)
	if got.err != nil || strings.Join(got.result.Logs, "\n") != "installed" {
		t.Fatalf("remaining client received %#v %v", got.result, got.err)
	}
	if cancelled.Load() {
		t.Fatal("run was cancelled although a client was still waiting")
	}
}

func TestNextRunWaitsForCancelledRunToStop(t *testing.T) {
	var running concurrency
	var calls atomic.Int32
	firstCancelled := make(chan struct{})
	finishFirst := make(chan struct{})
	var finishOnce sync.Once
	socket, runs := startTestHelper(t, func(ctx context.Context) (Result, error) {
		running.enter()
		defer running.exit()
		if calls.Add(1) == 1 {
			<-ctx.Done()
			close(firstCancelled)
			// A package manager command still completing after cancellation.
			<-finishFirst
			return Result{}, ctx.Err()
		}
		return Result{Logs: []string{"second run"}}, nil
	})
	// Registered after startTestHelper, so it runs before the helper stops.
	t.Cleanup(func() { finishOnce.Do(func() { close(finishFirst) }) })

	ctx, cancel := context.WithCancel(context.Background())
	first := ensureAsync(ctx, socket)
	waitFor(t, "the first run to start", func() bool { return calls.Load() == 1 })
	cancel()
	receive(t, "the cancelled client", first)
	receive(t, "the first run to be cancelled", firstCancelled)

	second := ensureAsync(context.Background(), socket)
	waitFor(t, "the next request to start a new run", func() bool {
		runs.mu.Lock()
		defer runs.mu.Unlock()
		return runs.current != nil && runs.current.waiters == 1
	})
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("a new run started while the cancelled run was still stopping")
	}
	finishOnce.Do(func() { close(finishFirst) })
	got := receive(t, "the next run", second)
	if got.err != nil || strings.Join(got.result.Logs, "\n") != "second run" {
		t.Fatalf("next request received %#v %v", got.result, got.err)
	}
	if running.maximum() != 1 {
		t.Fatalf("%d runs executed at the same time", running.maximum())
	}
}

func TestRunDoesNotStartCommandsAfterCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX commands")
	}
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, "touch", marker); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run returned %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command started after cancellation: %v", err)
	}
}

func TestRunLetsStartedCommandFinishButHonorsDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX commands")
	}
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type commandOutcome struct {
		output string
		err    error
	}
	finished := make(chan commandOutcome, 1)
	go func() {
		output, err := run(ctx, "sh", "-c", `touch "$1"; sleep 0.3; echo finished`, "sh", marker)
		finished <- commandOutcome{output: output, err: err}
	}()
	waitFor(t, "the command to start", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	cancel()
	if got := receive(t, "the started command", finished); got.err != nil || got.output != "finished" {
		t.Fatalf("cancellation interrupted a running command: %q %v", got.output, got.err)
	}

	deadlineCtx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	started := time.Now()
	if _, err := run(deadlineCtx, "sleep", "30"); err == nil {
		t.Fatal("command outlived its deadline")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("deadline did not stop the command: %s", elapsed)
	}
}
