// Package prerequisite installs the small, fixed set of dependencies required
// for Docker Compose deployments. It intentionally accepts no package names or
// shell snippets from callers.
package prerequisite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const DefaultSocket = "/run/wio-prerequisites/helper.sock"

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
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(15 * time.Minute))
	}
	if err := json.NewEncoder(connection).Encode(request{Action: "ensure"}); err != nil {
		return Result{}, err
	}
	var reply response
	if err := json.NewDecoder(connection).Decode(&reply); err != nil {
		return Result{}, err
	}
	if reply.Error != "" {
		return reply.Result, errors.New(reply.Error)
	}
	return reply.Result, nil
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
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handle(connection)
	}
}

func RunOnce(ctx context.Context) (Result, error) {
	if os.Geteuid() != 0 {
		return Result{}, errors.New("deployment prerequisite helper must run as root")
	}
	return ensure(ctx, run)
}

func handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(15 * time.Minute))
	var input request
	if err := json.NewDecoder(connection).Decode(&input); err != nil {
		return
	}
	reply := response{}
	if input.Action != "ensure" {
		reply.Error = "unsupported prerequisite helper action"
	} else {
		result, err := RunOnce(context.Background())
		reply.Result = result
		if err != nil {
			reply.Error = err.Error()
		}
	}
	_ = json.NewEncoder(connection).Encode(reply)
}

func ensure(ctx context.Context, runner Runner) (Result, error) {
	result := Result{}
	gitAvailable := commandWorks(ctx, runner, "git", "--version")
	dockerAvailable := commandWorks(ctx, runner, "docker", "info", "--format", "{{.ServerVersion}}")
	composeAvailable := commandWorks(ctx, runner, "docker", "compose", "version", "--short")
	if gitAvailable && dockerAvailable && composeAvailable {
		result.Logs = append(result.Logs, "deployment prerequisites are already available")
		return result, nil
	}
	manager, err := packageManager()
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

func run(ctx context.Context, command string, args ...string) (string, error) {
	process := exec.CommandContext(ctx, command, args...)
	output, err := process.CombinedOutput()
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
