package sshbootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestCodexConfiguration(t *testing.T) {
	configuration := codexConfiguration("https://api.example.com/v1/", "custom-model")
	for _, expected := range []string{`model = "custom-model"`, `model_provider = "wio_api"`, `model_supports_reasoning_summaries = true`, `base_url = "https://api.example.com/v1"`, `env_key = "WIO_CODEX_API_KEY"`, `wire_api = "responses"`} {
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

func TestConnectClearsHandshakeDeadline(t *testing.T) {
	host, port := startTestSSHServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	client, _, err := connect(ctx, Target{Host: host, Port: port, User: "root", AuthMethod: "password", Password: "test-password"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	<-ctx.Done()
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("SSH connection inherited the handshake deadline: %v", err)
	}
	defer session.Close()
	if err := session.Run("true"); err != nil {
		t.Fatalf("SSH command failed after the handshake deadline: %v", err)
	}
}

func startTestSSHServer(t *testing.T) (string, int) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	configuration := &ssh.ServerConfig{NoClientAuth: true}
	configuration.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		server, channels, requests, err := ssh.NewServerConn(connection, configuration)
		if err != nil {
			_ = connection.Close()
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for newChannel := range channels {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
				continue
			}
			channel, channelRequests, err := newChannel.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer channel.Close()
				for request := range channelRequests {
					if request.Type != "exec" {
						_ = request.Reply(false, nil)
						continue
					}
					_ = request.Reply(true, nil)
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: 0}))
					return
				}
			}()
		}
	}()
	host, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func TestProgressReaderReportsUploadCompletion(t *testing.T) {
	content := strings.Repeat("x", 700<<10)
	var updates []InstallProgress
	reader := &progressReader{
		reader: strings.NewReader(content), total: int64(len(content)), lastAt: time.Now(),
		notify: func(current, total int64) { updates = append(updates, InstallProgress{Current: current, Total: total}) },
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
	if len(updates) < 2 {
		t.Fatalf("expected incremental progress, got %d updates", len(updates))
	}
	last := updates[len(updates)-1]
	if last.Current != int64(len(content)) || last.Total != int64(len(content)) {
		t.Fatalf("unexpected final progress: %#v", last)
	}
}
