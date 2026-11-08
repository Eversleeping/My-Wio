// Package prerequisite installs the small, fixed set of dependencies required
// for Docker Compose deployments. It intentionally accepts no package names or
// shell snippets from callers.
package prerequisite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/childprocess"
)

const DefaultSocket = "/run/wio-prerequisites/helper.sock"

// requestTimeout bounds a single helper request, and also a single run: a
// package manager that hangs is eventually killed instead of blocking every
// later request.
const requestTimeout = 15 * time.Minute

type Result struct {
	Logs []string `json:"logs"`
}

type request struct {
	Action string `json:"action"`
}

type response struct {
	Result Result `json:"result"`
	Error  string `json:"error,omitempty"`
}

type Runner func(context.Context, string, ...string) (string, error)

// Ensure asks the helper listening on socket to install and verify the
// deployment prerequisites. Each connection carries one request; the client
// keeps it open until the reply arrives. Closing it earlier, which happens
// when ctx is cancelled, abandons the request, and the helper cancels the run
// when no other client is waiting for it.
func Ensure(ctx context.Context, socket string) (Result, error) {
	if socket == "" {
		socket = DefaultSocket
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return Result{}, err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(requestTimeout))
	}
	if err := json.NewEncoder(connection).Encode(request{Action: "ensure"}); err != nil {
		return Result{}, contextError(ctx, err)
	}
	var reply response
	if err := json.NewDecoder(connection).Decode(&reply); err != nil {
		return Result{}, contextError(ctx, err)
	}
	if reply.Error != "" {
		return reply.Result, errors.New(reply.Error)
	}
	return reply.Result, nil
}

// contextError reports the cancellation that closed the connection rather
// than the resulting "use of closed network connection".
func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func Serve(ctx context.Context, socket string) error {
	if os.Geteuid() != 0 {
		return errors.New("deployment prerequisite helper must run as root")
	}
	if socket == "" {
		socket = DefaultSocket
	}
	directory := filepath.Dir(socket)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	group, err := user.LookupGroup("wio-agent")
	if err != nil {
		return fmt.Errorf("lookup wio-agent group: %w", err)
	}
	var groupID int
	if _, err := fmt.Sscanf(group.Gid, "%d", &groupID); err != nil {
		return fmt.Errorf("parse wio-agent group: %w", err)
	}
	if err := os.Chown(directory, 0, groupID); err != nil {
		return err
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer func() {
		listener.Close()
		_ = os.Remove(socket)
	}()
	if err := os.Chown(socket, 0, groupID); err != nil {
		return err
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		return err
	}
	return serve(ctx, listener, newCoordinator(func(ctx context.Context) (Result, error) {
		return ensure(ctx, run)
	}))
}

// serve accepts helper requests until ctx is cancelled or accepting fails.
// Before returning it cancels the remaining runs and waits for them and for
// the request handlers, so a package manager command in progress completes.
func serve(ctx context.Context, listener net.Listener, runs *coordinator) error {
	ctx, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup
	defer func() {
		cancel()
		handlers.Wait()
		runs.wait()
	}()
	context.AfterFunc(ctx, func() { _ = listener.Close() })
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			runs.handle(ctx, connection)
		}()
	}
}

func RunOnce(ctx context.Context) (Result, error) {
	if os.Geteuid() != 0 {
		return Result{}, errors.New("deployment prerequisite helper must run as root")
	}
	return ensure(ctx, run)
}

// coordinator lets concurrent requests share one prerequisite run. Package
// managers hold an exclusive lock, so parallel runs (one per deployment that
// found something missing) would only fail on, or wait for, each other.
type coordinator struct {
	ensure  func(context.Context) (Result, error)
	timeout time.Duration
	// runLock is held while a run executes. A run whose clients all went away
	// is cancelled but finishes its current command first; the next run waits
	// for it instead of competing for the package manager lock.
	runLock chan struct{}
	runs    sync.WaitGroup

	mu      sync.Mutex
	current *flight // the run new requests join, if any
}

// flight is one prerequisite run and the requests waiting for its result.
type flight struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{} // closed once result and err are set
	result  Result
	err     error
	waiters int // guarded by coordinator.mu
}

func newCoordinator(ensure func(context.Context) (Result, error)) *coordinator {
	return &coordinator{ensure: ensure, timeout: requestTimeout, runLock: make(chan struct{}, 1)}
}

func (c *coordinator) handle(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(requestTimeout))
	var input request
	if err := json.NewDecoder(connection).Decode(&input); err != nil {
		return
	}
	reply := response{}
	if input.Action != "ensure" {
		reply.Error = "unsupported prerequisite helper action"
	} else {
		result, err := c.ensureFor(ctx, connection)
		reply.Result = result
		if err != nil {
			reply.Error = err.Error()
		}
	}
	_ = json.NewEncoder(connection).Encode(reply)
}

// ensureFor joins the current run, or starts one, on behalf of the client on
// connection and returns its result. It gives up once the client disconnects.
func (c *coordinator) ensureFor(ctx context.Context, connection net.Conn) (Result, error) {
	run := c.join(ctx)
	disconnected := make(chan struct{})
	go func() {
		// The client sends nothing after its request, so reading returns only
		// when it closes the connection, the request deadline passes, or the
		// handler closes the connection after replying.
		_, _ = io.Copy(io.Discard, connection)
		close(disconnected)
	}()
	select {
	case <-run.done:
		return run.result, run.err
	case <-disconnected:
		c.leave(run)
		return Result{}, errors.New("client disconnected before prerequisite setup finished")
	}
}

// join registers a waiter for the current run, starting a run if none is in
// progress.
func (c *coordinator) join(ctx context.Context) *flight {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		runCtx, cancel := context.WithTimeout(ctx, c.timeout)
		c.current = &flight{ctx: runCtx, cancel: cancel, done: make(chan struct{})}
		c.runs.Add(1)
		go c.execute(c.current)
	}
	c.current.waiters++
	return c.current
}

// leave unregisters a waiter whose client went away before the run finished.
// A run nobody waits for is cancelled and detached, so later requests start a
// fresh run (after this one has stopped) instead of inheriting its
// cancellation.
func (c *coordinator) leave(run *flight) {
	c.mu.Lock()
	defer c.mu.Unlock()
	run.waiters--
	if run.waiters > 0 {
		return
	}
	if c.current == run {
		c.current = nil
	}
	run.cancel()
}

func (c *coordinator) execute(run *flight) {
	defer c.runs.Done()
	select {
	case c.runLock <- struct{}{}:
		if run.err = run.ctx.Err(); run.err == nil {
			run.result, run.err = c.ensure(run.ctx)
		}
		<-c.runLock
	case <-run.ctx.Done():
		run.err = run.ctx.Err()
	}
	c.mu.Lock()
	if c.current == run {
		c.current = nil
	}
	c.mu.Unlock()
	run.cancel()
	close(run.done)
}

// wait blocks until every started run has finished.
func (c *coordinator) wait() {
	c.runs.Wait()
}

func ensure(ctx context.Context, runner Runner) (Result, error) {
	return ensureWithManager(ctx, runner, packageManager)
}

func ensureWithManager(ctx context.Context, runner Runner, detectManager func() (string, error)) (Result, error) {
	result := Result{}
	gitAvailable := commandWorks(ctx, runner, "git", "--version")
	dockerAvailable := commandWorks(ctx, runner, "docker", "info", "--format", "{{.ServerVersion}}")
	composeAvailable := commandWorks(ctx, runner, "docker", "compose", "version", "--short")
	if gitAvailable && dockerAvailable && composeAvailable {
		result.Logs = append(result.Logs, "deployment prerequisites are already available")
		return result, nil
	}
	manager, err := detectManager()
	if err != nil {
		return result, err
	}
	if !gitAvailable {
		if err := install(ctx, runner, manager, []string{"git"}, &result); err != nil {
			return result, fmt.Errorf("install Git: %w", err)
		}
	}
	if !dockerAvailable {
		if err := installDocker(ctx, runner, manager, &result); err != nil {
			return result, err
		}
		if output, err := runner(ctx, "systemctl", "enable", "--now", "docker"); err != nil {
			result.Logs = append(result.Logs, commandLog("systemctl enable --now docker", output))
			return result, fmt.Errorf("start Docker service: %w", err)
		}
		result.Logs = append(result.Logs, "Docker service started")
	}
	if !composeAvailable {
		if err := installCompose(ctx, runner, manager, &result); err != nil {
			return result, err
		}
	}
	for _, check := range []struct {
		name string
		args []string
	}{
		{"git", []string{"--version"}},
		{"docker", []string{"info", "--format", "{{.ServerVersion}}"}},
		{"docker", []string{"compose", "version", "--short"}},
	} {
		if !commandWorks(ctx, runner, check.name, check.args...) {
			return result, fmt.Errorf("prerequisite verification failed: %s %s", check.name, strings.Join(check.args, " "))
		}
	}
	return result, nil
}

func packageManager() (string, error) {
	for _, name := range []string{"apt-get", "dnf", "yum"} {
		if _, err := exec.LookPath(name); err == nil {
			return name, nil
		}
	}
	return "", errors.New("automatic setup supports only apt-get, dnf, or yum")
}

func installDocker(ctx context.Context, runner Runner, manager string, result *Result) error {
	var candidates []string
	switch manager {
	case "apt-get":
		candidates = []string{"docker.io"}
	default:
		candidates = []string{"moby-engine", "docker", "docker-ce"}
	}
	if err := installAny(ctx, runner, manager, candidates, result); err != nil {
		return fmt.Errorf("install Docker Engine: %w", err)
	}
	return nil
}

func installCompose(ctx context.Context, runner Runner, manager string, result *Result) error {
	candidates := []string{"docker-compose-plugin", "docker-compose-v2"}
	if manager != "apt-get" {
		candidates = append(candidates, "docker-compose")
	}
	if err := installAny(ctx, runner, manager, candidates, result); err != nil {
		return fmt.Errorf("install Docker Compose: %w", err)
	}
	if !commandWorks(ctx, runner, "docker", "compose", "version", "--short") {
		return errors.New("installed package did not provide Docker Compose v2")
	}
	result.Logs = append(result.Logs, "Docker Compose installed")
	return nil
}

func installAny(ctx context.Context, runner Runner, manager string, candidates []string, result *Result) error {
	var failures []string
	for _, candidate := range candidates {
		if err := install(ctx, runner, manager, []string{candidate}, result); err == nil {
			return nil
		} else {
			failures = append(failures, candidate+": "+err.Error())
		}
	}
	return errors.New(strings.Join(failures, "; "))
}

func install(ctx context.Context, runner Runner, manager string, packages []string, result *Result) error {
	if manager == "apt-get" {
		output, err := runner(ctx, manager, "update")
		result.Logs = append(result.Logs, commandLog("apt-get update", output))
		if err != nil {
			return err
		}
	}
	args := append([]string{"install", "-y"}, packages...)
	output, err := runner(ctx, manager, args...)
	result.Logs = append(result.Logs, commandLog(manager+" "+strings.Join(args, " "), output))
	if err == nil {
		result.Logs = append(result.Logs, strings.Join(packages, ", ")+" installed")
	}
	return err
}

func commandWorks(ctx context.Context, runner Runner, command string, args ...string) bool {
	_, err := runner(ctx, command, args...)
	return err == nil
}

// run executes one helper command. Cancellation (every client went away, or
// the helper is stopping) keeps further commands from starting but does not
// interrupt one already running: killing apt-get, dpkg, dnf or yum halfway
// through a transaction can leave the package database broken for every
// later run. A deadline still kills the command, so a hung package manager
// cannot block the helper forever.
func run(ctx context.Context, command string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	processCtx := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		processCtx, cancel = context.WithDeadline(processCtx, deadline)
		defer cancel()
	}
	process := childprocess.CommandContext(processCtx, command, args...)
	output, err := childprocess.CombinedOutput(process)
	return strings.TrimSpace(string(output)), err
}

func commandLog(command, output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return "ran: " + command
	}
	if len(output) > 8192 {
		output = output[:8192] + "..."
	}
	return "ran: " + command + "\n" + output
}
