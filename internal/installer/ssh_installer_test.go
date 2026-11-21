package installer

import (
	"context"
	"encoding/json"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHDelegatesToTargetInstallerWithPlanOnStdin(t *testing.T) {
	transferred := make(chan InstallRequest, 1)
	host, port := startExecSSHServer(t, func(channel ssh.Channel, command string) {
		raw, _ := io.ReadAll(channel)
		for _, secret := range []string{"one-time-secret", "api-key-secret"} {
			if strings.Contains(command, secret) {
				t.Error("SSH command line exposed credentials")
			}
		}
		switch {
		case command == "hostname":
			_, _ = io.WriteString(channel, "node.local")
		case command == "uname -s":
			_, _ = io.WriteString(channel, "Linux")
		case command == "uname -m":
			_, _ = io.WriteString(channel, "x86_64")
		case command == "id -u":
			_, _ = io.WriteString(channel, "0")
		case strings.Contains(command, "mktemp -d"):
			_, _ = io.WriteString(channel, "/tmp/wio-install.abcdefghij")
		case strings.Contains(command, "install --request-stdin"):
			var request InstallRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
			}
			transferred <- request
			encoder := json.NewEncoder(channel)
			_ = encoder.Encode(Event{Type: "progress", Step: "waiting_agent"})
			_ = encoder.Encode(Event{Type: "complete", Result: &InstallResult{ServerID: "11111111-1111-4111-8111-111111111111", Architecture: "amd64", Warnings: []string{}}})
		}
	})
	client, key, err := connect(context.Background(), Target{Host: host, Port: port, User: "root", AuthMethod: "none"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "wio-agent-linux-amd64"), []byte("target-installer"), 0600); err != nil {
		t.Fatal(err)
	}
	progress := []string{}
	result, err := New(assets).launchInstaller(context.Background(), client, key, InstallRequest{Target: Target{Password: "ssh-login-secret"}, EnrollmentToken: "one-time-secret", CodexAPIKey: "api-key-secret", Versions: CLIVersions{Codex: "1.0.0", Gemini: "1.0.0", Claude: "1.0.0"}, Progress: func(p InstallProgress) { progress = append(progress, p.Step) }})
	if err != nil || result.ServerID == "" {
		t.Fatalf("target install failed: %+v %v", result, err)
	}
	plan := <-transferred
	if plan.Target.Password != "" || plan.EnrollmentToken != "one-time-secret" || plan.CodexAPIKey != "api-key-secret" {
		t.Fatal("SSH transport lost the installation plan or forwarded login credentials")
	}
	if len(progress) == 0 || progress[len(progress)-1] != "waiting_agent" {
		t.Fatal("target progress did not reach the web transport")
	}
}
