package prerequisite

import (
	"context"
	"errors"
	"strings"
	"testing"
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
