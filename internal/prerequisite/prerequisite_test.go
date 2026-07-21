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
