package sshbootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
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

func TestAgentServiceUnitAllowsExplicitSudo(t *testing.T) {
	base := []byte("NoNewPrivileges=true\nProtectSystem=strict\nProtectHome=read-only\nRestrictSUIDSGID=true\n")
	if got := string(agentServiceUnit(base, false)); got != string(base) {
		t.Fatalf("default unit changed: %q", got)
	}
	enabled := string(agentServiceUnit(base, true))
	for _, expected := range []string{"NoNewPrivileges=false", "ProtectSystem=false", "ProtectHome=false", "RestrictSUIDSGID=false"} {
		if !strings.Contains(enabled, expected) {
			t.Fatalf("sudo-enabled unit missing %q: %s", expected, enabled)
		}
	}
}

func TestPrerequisiteServiceCreatesSocketRuntimeDirectory(t *testing.T) {
	unit, err := os.ReadFile(filepath.Join("..", "..", "deploy", "prerequisite.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"RuntimeDirectory=wio-prerequisites", "RuntimeDirectoryMode=0750", "ReadWritePaths=/run/wio-prerequisites"} {
		if !strings.Contains(string(unit), expected) {
			t.Fatalf("prerequisite unit missing %q", expected)
		}
	}
}

func TestNPMRegistryForCountries(t *testing.T) {
	tests := []struct {
		name      string
		countries string
		want      string
	}{
		{name: "mainland consensus", countries: "CN\nCN\n", want: mainlandNPMRegistry},
		{name: "single mainland result", countries: "CN\n", want: mainlandNPMRegistry},
		{name: "international consensus", countries: "US\nUS\n", want: officialNPMRegistry},
		{name: "conflicting result", countries: "CN\nUS\n", want: officialNPMRegistry},
		{name: "unknown location", countries: "request failed", want: officialNPMRegistry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := npmRegistryForCountries(test.countries); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestNPMRegistryConfigurationScript(t *testing.T) {
	script := npmRegistryConfigurationScript(mainlandNPMRegistry)
	for _, expected := range []string{
		"registry 'https://registry.npmmirror.com'",
		"replace-registry-host always",
		"--userconfig=/root/.npmrc",
		"--userconfig=/var/lib/wio-agent/.npmrc",
		"chown wio-agent:wio-agent /var/lib/wio-agent/.npmrc",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("npm configuration script missing %q:\n%s", expected, script)
		}
	}
}

func TestPlaywrightInstallationScripts(t *testing.T) {
	packageScript := playwrightPackageInstallationScript()
	for _, expected := range []string{
		"playwright@" + playwrightVersion,
		"su -s /bin/sh",
		"/usr/local/bin/playwright",
		playwrightCLI(),
	} {
		if !strings.Contains(packageScript, expected) {
			t.Fatalf("Playwright package script missing %q:\n%s", expected, packageScript)
		}
	}
	if dependencies := playwrightDependenciesInstallationScript(); !strings.Contains(dependencies, "install-deps chromium") {
		t.Fatalf("Playwright dependency script does not install Chromium dependencies:\n%s", dependencies)
	}
	if browser := playwrightBrowserInstallationScript(mainlandNPMRegistry); !strings.Contains(browser, "PLAYWRIGHT_BROWSERS_PATH=/var/lib/wio-agent/.cache/ms-playwright") || !strings.Contains(browser, "PLAYWRIGHT_DOWNLOAD_HOST=") || !strings.Contains(browser, playwrightMirror) || !strings.Contains(browser, "timeout 10m") || !strings.Contains(browser, "install chromium") {
		t.Fatalf("Playwright browser script does not install Chromium as wio-agent:\n%s", browser)
	}
	if browser := playwrightBrowserInstallationScript(officialNPMRegistry); strings.Contains(browser, "PLAYWRIGHT_DOWNLOAD_HOST") {
		t.Fatalf("international Playwright installation unexpectedly uses the mainland mirror:\n%s", browser)
	}
	if verification := playwrightVerificationScript(); !strings.Contains(verification, "chromium.launch") || !strings.Contains(verification, "--version") {
		t.Fatalf("Playwright verification does not launch Chromium:\n%s", verification)
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

func TestGitCredentialEscapesUsernameAndToken(t *testing.T) {
	credential, err := gitCredential("https://github.com", "user@example.com", "token:/with special")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(credential)
	if err != nil {
		t.Fatal(err)
	}
	password, ok := parsed.User.Password()
	if !ok || parsed.User.Username() != "user@example.com" || password != "token:/with special" || parsed.Host != "github.com" {
		t.Fatalf("unexpected Git credential URL: %q", credential)
	}
}

func TestValidateInstallRequestRequiresGitCommitIdentity(t *testing.T) {
	request := InstallRequest{
		Target: Target{Host: "192.0.2.10", Port: 22, User: "root", AuthMethod: "password", Password: "ssh-password"}, ExpectedFingerprint: "SHA256:example",
		ControlURL: "https://wio.example.com", EnrollmentToken: "enrollment-token", CodexAPIURL: "https://api.example.com/v1", CodexAPIKey: "api-key-value", CodexModel: "gpt-5.6-sol",
		GitEndpoint: "https://github.com", GitUsername: "git-user", GitToken: "git-token-value",
	}
	if !errors.Is(validateInstallRequest(request), ErrInvalidTarget) {
		t.Fatal("Git credentials without commit identity were accepted")
	}
	request.GitCommitName = "Example User"
	request.GitCommitEmail = "user@example.com"
	if err := validateInstallRequest(request); err != nil {
		t.Fatalf("valid Git commit identity was rejected: %v", err)
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
