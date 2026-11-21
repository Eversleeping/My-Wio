package claudeadapter

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestWindowsMCPHeadersUsePrivateFile(t *testing.T) {
	secret := []byte(`{"mcpServers":{"wio":{"headers":{"Authorization":"private-test-token"}}}}`)
	path, cleanup, err := mcpConfigArgument(&exec.Cmd{}, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.Contains(path, "private-test-token") {
		t.Fatal("MCP token in command argument")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(secret) {
		t.Fatal("MCP file unavailable")
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("MCP credential file survived cleanup")
	}
}
