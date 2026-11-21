package installer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingHost struct {
	commands []string
	uploads  map[string]string
}

func (h *recordingHost) execute(command string, input io.Reader, output io.Writer) error {
	h.commands = append(h.commands, command)
	raw, _ := io.ReadAll(input)
	if len(raw) > 0 {
		h.uploads[command] = string(raw)
	}
	response := ""
	switch {
	case command == "hostname":
		response = "node.local"
	case command == "uname -s":
		response = "Linux"
	case command == "uname -m":
		response = "x86_64"
	case command == "id -u":
		response = "0"
	case strings.Contains(command, " enroll "):
		response = "Enrolled server 11111111-1111-4111-8111-111111111111; configuration written"
	case strings.Contains(command, "--version") && strings.Contains(command, "/versions/"):
		response = "1.0.0"
	}
	_, err := io.WriteString(output, response)
	return err
}
func TestSharedPipelineInstallsCredentialsDependenciesAndServices(t *testing.T) {
	assets := t.TempDir()
	for name, data := range map[string]string{"wio-agent-linux-amd64": "agent-binary", "wio-agent.service": "[Service]\nUser=wio-agent\nNoNewPrivileges=true\n", "wio-prerequisite.service": "[Service]\nUser=root\n"} {
		if err := os.WriteFile(filepath.Join(assets, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	host := &recordingHost{uploads: map[string]string{}}
	request := InstallRequest{ControlURL: "https://wio.example.test", EnrollmentToken: "device-key-secret", RegistrationID: "22222222-2222-4222-8222-222222222222", CodexAPIURL: "https://api.example.test/v1", CodexAPIKey: "api-key-secret", CodexModel: "gpt-6.1-sol", GitEndpoint: "https://git.example.test", GitUsername: "git-user", GitToken: "git-token-secret", GitCommitName: "Wio", GitCommitEmail: "wio@example.test", Versions: CLIVersions{Codex: "1.0.0", Gemini: "1.0.0", Claude: "1.0.0"}}
	result, err := New(assets).installOnHost(context.Background(), host, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServerID != "11111111-1111-4111-8111-111111111111" {
		t.Fatal("pipeline lost enrolled identity")
	}
	all := strings.Join(host.commands, "\n")
	for _, secret := range []string{"device-key-secret", "api-key-secret", "git-token-secret"} {
		if strings.Contains(all, secret) {
			t.Fatal("secret in target command arguments")
		}
	}
	for _, required := range []string{"useradd --system", "/var/lib/wio-agent/.gitconfig", "--registration", "systemctl enable wio-prerequisite wio-agent", "/var/lib/wio-agent/codex/versions/1.0.0"} {
		if !strings.Contains(all, required) {
			t.Fatalf("shared pipeline omitted %s", required)
		}
	}
	keyWritten, tokenStdin := false, false
	for command, body := range host.uploads {
		if strings.Contains(command, "codex.key") && body == "api-key-secret\n" {
			keyWritten = true
		}
		if strings.Contains(command, " enroll ") && body == "device-key-secret\n" {
			tokenStdin = true
		}
	}
	if !keyWritten || !tokenStdin {
		t.Fatal("credentials were not delivered through protected files/stdin")
	}
}
