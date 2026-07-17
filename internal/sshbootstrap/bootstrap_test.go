package sshbootstrap

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestNormalizeTargetAndArchitecture(t *testing.T) {
	target, err := normalizeTarget(Target{Host: " server.example.com ", User: "ubuntu", AuthMethod: "password", Password: "secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Host != "server.example.com" || target.Port != 22 {
		t.Fatalf("unexpected normalized target: %#v", target)
	}
	if asset, err := agentAsset("x86_64"); err != nil || asset != "wio-agent-linux-amd64" {
		t.Fatalf("unexpected amd64 asset: %q %v", asset, err)
	}
	if asset, err := agentAsset("aarch64"); err != nil || asset != "wio-agent-linux-arm64" {
		t.Fatalf("unexpected arm64 asset: %q %v", asset, err)
	}
	if _, err := agentAsset("riscv64"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("unexpected unsupported architecture error: %v", err)
	}
}

func TestValidateInstallRequest(t *testing.T) {
	request := InstallRequest{
		Target:              Target{Host: "192.0.2.10", Port: 22, User: "root", AuthMethod: "password", Password: "ssh-password"},
		ExpectedFingerprint: "SHA256:example", ControlURL: "https://wio.example.com", EnrollmentToken: "enrollment-token",
		CodexAPIURL: "https://api.example.com/v1", CodexAPIKey: "api-key-value", CodexModel: "gpt-5.4",
	}
	if err := validateInstallRequest(request); err != nil {
		t.Fatal(err)
	}
	request.CodexAPIURL = "https://user:password@example.com/v1"
	if !errors.Is(validateInstallRequest(request), ErrInvalidTarget) {
		t.Fatal("URL credentials must be rejected")
	}
	request.CodexAPIURL = "https://api.example.com/v1"
	request.CodexAPIKey = "bad\nkey"
	if !errors.Is(validateInstallRequest(request), ErrInvalidTarget) {
		t.Fatal("multiline API keys must be rejected")
	}
}

func TestCodexConfigurationDoesNotContainKey(t *testing.T) {
	configuration := codexConfiguration("https://api.example.com/v1/", "custom-model")
	for _, expected := range []string{`model = "custom-model"`, `model_provider = "wio_api"`, `base_url = "https://api.example.com/v1"`, `env_key = "WIO_CODEX_API_KEY"`, `wire_api = "responses"`} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("configuration missing %q:\n%s", expected, configuration)
		}
	}
	if strings.Contains(configuration, "api-key-value") {
		t.Fatal("configuration must not contain an API key")
	}
}

func TestEnrollmentServerID(t *testing.T) {
	id := "6d22a53c-6a88-46ec-a201-1fd24dd83bea"
	if actual := enrollmentServerID("Enrolled server " + id + "; configuration written"); actual != id {
		t.Fatalf("got %q", actual)
	}
	if actual := enrollmentServerID("unexpected output"); actual != "" {
		t.Fatalf("got %q", actual)
	}
}

func TestClassifyHandshakeError(t *testing.T) {
	authenticationError := &ssh.ServerAuthError{Errors: []error{errors.New("password rejected")}}
	if err := classifyHandshakeError(authenticationError); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected authentication error, got %v", err)
	}
	if err := classifyHandshakeError(errors.New("handshake failed")); !errors.Is(err, ErrConnection) {
		t.Fatalf("expected connection error, got %v", err)
	}
}
