//go:build !windows

package claudeadapter

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeMCPAndAPIKeyUseSeparateDescriptors(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CLAUDE_TEST_CHILD", "success")
	t.Setenv("CLAUDE_TEST_RECORD", filepath.Join(directory, "input.json"))
	keyRecord, mcpRecord := filepath.Join(directory, "key.json"), filepath.Join(directory, "mcp.json")
	t.Setenv("CLAUDE_TEST_KEY_RECORD", keyRecord)
	t.Setenv("CLAUDE_TEST_MCP_RECORD", mcpRecord)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := protocol.ClaudeTurnCommand{
		Workspace: directory, PermissionMode: "default",
		CollaborationMCP: map[string]any{"name": "wio", "url": "http://127.0.0.1:12345/mcp", "headers": map[string]string{"Authorization": "Bearer test-mcp-secret"}},
	}
	if err := Run(context.Background(), binary, "operation", command, func(protocol.StreamEvent) error { return nil }, "ANTHROPIC_API_KEY=test-api-secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(mcpRecord)
	if err != nil {
		t.Fatal(err)
	}
	var delivery struct {
		Source        string
		Args          []string
		Configuration struct {
			Servers map[string]struct {
				URL     string
				Headers map[string]string
			} `json:"mcpServers"`
		}
	}
	if err := json.Unmarshal(raw, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.Source != "/dev/fd/4" || strings.Contains(strings.Join(delivery.Args, " "), "test-mcp-secret") {
		t.Fatal("MCP configuration did not use its separate anonymous descriptor")
	}
	server := delivery.Configuration.Servers["wio"]
	if server.URL != "http://127.0.0.1:12345/mcp" || server.Headers["Authorization"] != "Bearer test-mcp-secret" {
		t.Fatal("CLI did not receive the MCP endpoint and authorization")
	}
	raw, err = os.ReadFile(keyRecord)
	if err != nil {
		t.Fatal(err)
	}
	var key map[string]string
	if err := json.Unmarshal(raw, &key); err != nil {
		t.Fatal(err)
	}
	if key["environment"] != "" || key["descriptor"] != "3" || key["key"] != "test-api-secret" {
		t.Fatal("MCP configuration interfered with API key delivery")
	}
}

func TestMCPConfigurationDescriptorIsReleased(t *testing.T) {
	cmd := newTestCommand()
	configuration := []byte(`{"mcpServers":{}}`)
	source, release, err := mcpConfigArgument(cmd, configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if source != "/dev/fd/3" || len(cmd.ExtraFiles) != 1 {
		t.Fatal("MCP configuration did not allocate an anonymous descriptor")
	}
	raw, err := io.ReadAll(cmd.ExtraFiles[0])
	if err != nil || string(raw) != string(configuration) {
		t.Fatal("MCP configuration descriptor was incomplete")
	}
	release()
	if _, err := cmd.ExtraFiles[0].Stat(); err == nil {
		t.Fatal("release left the parent configuration descriptor open")
	}
}
