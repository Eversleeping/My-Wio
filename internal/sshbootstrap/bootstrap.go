package sshbootstrap

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/wio-platform/wio/internal/gitidentity"
)

var (
	ErrInvalidTarget       = errors.New("invalid SSH target")
	ErrHostKeyMismatch     = errors.New("SSH host key fingerprint changed")
	ErrConnection          = errors.New("SSH connection failed")
	ErrAuthentication      = errors.New("SSH authentication failed")
	ErrPrivilegeRequired   = errors.New("root or passwordless sudo is required")
	ErrUnsupportedPlatform = errors.New("unsupported Linux architecture")
	ErrAssetsUnavailable   = errors.New("agent installation assets are unavailable")
	ErrInstallation        = errors.New("agent installation failed")
)

type Target struct {
	Host                 string `json:"host"`
	Port                 int    `json:"port"`
	User                 string `json:"user"`
	AuthMethod           string `json:"auth_method"`
	Password             string `json:"password,omitempty"`
	PrivateKey           string `json:"private_key,omitempty"`
	PrivateKeyPassphrase string `json:"private_key_passphrase,omitempty"`
}

type HostTarget struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type HostKeyResult struct {
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"key_type"`
}

type InstallRequest struct {
	Target              Target
	ExpectedFingerprint string
	ControlURL          string
	EnrollmentToken     string
	CodexAPIURL         string
	CodexAPIKey         string
	CodexModel          string
	GitEndpoint         string
	GitUsername         string
	GitToken            string
	GitCommitName       string
	GitCommitEmail      string
	Progress            func(InstallProgress)
}

type InstallProgress struct {
	Step    string `json:"step"`
	Current int64  `json:"current,omitempty"`
	Total   int64  `json:"total,omitempty"`
}

type InstallResult struct {
	ServerID     string   `json:"server_id"`
	Hostname     string   `json:"hostname"`
	Architecture string   `json:"architecture"`
	Warnings     []string `json:"warnings"`
}

type Service struct {
	assetDir string
}

func New(assetDir string) *Service {
	if strings.TrimSpace(assetDir) == "" {
		assetDir = "/usr/local/share/wio"
	}
	return &Service{assetDir: assetDir}
}

func (s *Service) Probe(ctx context.Context, target HostTarget) (HostKeyResult, error) {
	target, err := normalizeHostTarget(target)
	if err != nil {
		return HostKeyResult{}, err
	}
	return probeHostKey(ctx, target)
}

func (s *Service) Install(ctx context.Context, request InstallRequest) (InstallResult, error) {
	target, err := normalizeTarget(request.Target)
	if err != nil {
		return InstallResult{}, err
	}
	request.Target = target
	if err := validateInstallRequest(request); err != nil {
		return InstallResult{}, err
	}
	request.report("connecting", 0, 0)
	client, hostKey, err := connect(ctx, request.Target, request.ExpectedFingerprint)
	if err != nil {
		return InstallResult{}, err
	}
	clientDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-clientDone:
		}
	}()
	defer func() {
		close(clientDone)
		_ = client.Close()
	}()
	request.report("inspecting", 0, 0)
	probe, err := inspect(client, hostKey)
	if err != nil {
		return InstallResult{}, err
	}
	if !strings.EqualFold(probe.OS, "linux") {
		return InstallResult{}, fmt.Errorf("%w: %s", ErrUnsupportedPlatform, probe.OS)
	}
	if !probe.SudoReady {
		return InstallResult{}, ErrPrivilegeRequired
	}

	assetName, err := agentAsset(probe.Architecture)
	if err != nil {
		return InstallResult{}, err
	}
	agentBinary, err := os.ReadFile(filepath.Join(s.assetDir, assetName))
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: %v", ErrAssetsUnavailable, err)
	}
	unit, err := os.ReadFile(filepath.Join(s.assetDir, "wio-agent.service"))
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: %v", ErrAssetsUnavailable, err)
	}

	root := isRoot(client)
	request.report("uploading_agent", 0, int64(len(agentBinary)))
	if err := upload(client, root, "/usr/local/bin/wio-agent", "0755", agentBinary, func(current, total int64) {
		request.report("uploading_agent", current, total)
	}); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install agent binary: %v", ErrInstallation, err)
	}
	request.report("uploading_service", 0, int64(len(unit)))
	if err := upload(client, root, "/etc/systemd/system/wio-agent.service", "0644", unit, func(current, total int64) {
		request.report("uploading_service", current, total)
	}); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install systemd unit: %v", ErrInstallation, err)
	}
	request.report("preparing_account", 0, 0)
	setup := `set -eu
getent group docker >/dev/null 2>&1 || groupadd --system docker
id -u wio-agent >/dev/null 2>&1 || useradd --system --home /var/lib/wio-agent --create-home --shell /usr/sbin/nologin wio-agent
usermod -aG docker wio-agent
install -d -o wio-agent -g wio-agent -m 0750 /var/lib/wio-agent
install -d -o wio-agent -g wio-agent -m 0700 /var/lib/wio-agent/.codex
install -d -o root -g wio-agent -m 0750 /etc/wio-agent`
	if _, err := run(client, elevated(root, setup)); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not prepare service account", ErrInstallation)
	}
	request.report("configuring_codex", 0, 0)
	if err := upload(client, root, "/etc/wio-agent/codex.key", "0600", []byte(request.CodexAPIKey+"\n"), nil); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install Codex credentials: %v", ErrInstallation, err)
	}
	if err := upload(client, root, "/var/lib/wio-agent/.codex/config.toml", "0600", []byte(codexConfiguration(request.CodexAPIURL, request.CodexModel)), nil); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install Codex configuration: %v", ErrInstallation, err)
	}
	if _, err := run(client, elevated(root, "chown wio-agent:wio-agent /var/lib/wio-agent/.codex/config.toml")); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not protect Codex configuration", ErrInstallation)
	}
	if request.GitEndpoint != "" {
		request.report("configuring_git", 0, 0)
		credential, err := gitCredential(request.GitEndpoint, request.GitUsername, request.GitToken)
		if err != nil {
			return InstallResult{}, ErrInvalidTarget
		}
		if err := upload(client, root, "/var/lib/wio-agent/.git-credentials", "0600", []byte(credential+"\n"), nil); err != nil {
			return InstallResult{}, fmt.Errorf("%w: could not install Git credentials: %v", ErrInstallation, err)
		}
		gitConfig, err := gitidentity.Configuration(request.GitCommitName, request.GitCommitEmail, "/var/lib/wio-agent/.git-credentials")
		if err != nil {
			return InstallResult{}, ErrInvalidTarget
		}
		if err := upload(client, root, "/var/lib/wio-agent/.gitconfig", "0600", []byte(gitConfig), nil); err != nil {
			return InstallResult{}, fmt.Errorf("%w: could not install Git configuration: %v", ErrInstallation, err)
		}
		if _, err := run(client, elevated(root, "chown wio-agent:wio-agent /var/lib/wio-agent/.git-credentials /var/lib/wio-agent/.gitconfig")); err != nil {
			return InstallResult{}, fmt.Errorf("%w: could not protect Git credentials", ErrInstallation)
		}
	}

	codexReady := probe.CodexReady
	resultWarnings := make([]string, 0, 4)
	if !codexReady && probe.NPMReady {
		request.report("installing_codex", 0, 0)
		if _, installErr := run(client, elevated(root, "npm install --global @openai/codex@0.139.0")); installErr == nil {
			codexReady = commandAvailable(client, "codex")
		} else {
			resultWarnings = append(resultWarnings, "codex_install_failed")
		}
	}
	if !codexReady && !probe.NPMReady {
		resultWarnings = append(resultWarnings, "codex_install_requires_npm")
	}

	request.report("enrolling_agent", 0, 0)
	enroll := "/usr/local/bin/wio-agent enroll --url " + shellQuote(request.ControlURL) + " --token " + shellQuote(request.EnrollmentToken)
	output, err := run(client, elevated(root, enroll))
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: agent enrollment was rejected", ErrInstallation)
	}
	serverID := enrollmentServerID(output)
	if serverID == "" {
		return InstallResult{}, fmt.Errorf("%w: enrollment response was not recognized", ErrInstallation)
	}

	result := InstallResult{ServerID: serverID, Hostname: probe.Hostname, Architecture: normalizedArchitecture(probe.Architecture), Warnings: resultWarnings}
	permissions := "chown wio-agent:wio-agent /etc/wio-agent/config.json /etc/wio-agent/codex.key && chmod 0600 /etc/wio-agent/config.json /etc/wio-agent/codex.key"
	if _, err := run(client, elevated(root, permissions)); err != nil {
		result.Warnings = append(result.Warnings, "config_permission_failed")
	}
	request.report("starting_service", 0, 0)
	start := "systemctl daemon-reload && systemctl enable --now wio-agent"
	if _, err := run(client, elevated(root, start)); err != nil {
		result.Warnings = append(result.Warnings, "service_start_failed")
	} else if _, err := run(client, "systemctl is-active --quiet wio-agent"); err != nil {
		result.Warnings = append(result.Warnings, "service_not_active")
	}
	if !probe.DockerReady {
		result.Warnings = append(result.Warnings, "docker_unavailable")
	}
	if !probe.GitReady {
		result.Warnings = append(result.Warnings, "git_unavailable")
	}
	if !codexReady {
		result.Warnings = append(result.Warnings, "codex_unavailable")
	}
	return result, nil
}

func (request InstallRequest) report(step string, current, total int64) {
	if request.Progress != nil {
		request.Progress(InstallProgress{Step: step, Current: current, Total: total})
	}
}

func validateInstallRequest(request InstallRequest) error {
	if _, err := normalizeTarget(request.Target); err != nil {
		return err
	}
	if !strings.HasPrefix(request.ExpectedFingerprint, "SHA256:") || strings.TrimSpace(request.ControlURL) == "" || strings.TrimSpace(request.EnrollmentToken) == "" {
		return ErrInvalidTarget
	}
	if !validAPIURL(request.CodexAPIURL) || !validAPIKey(request.CodexAPIKey) || !validModel(request.CodexModel) {
		return ErrInvalidTarget
	}
	if request.GitEndpoint != "" {
		if !validAPIURL(request.GitEndpoint) || strings.TrimSpace(request.GitUsername) == "" || len(request.GitUsername) > 256 || strings.ContainsAny(request.GitUsername, "\r\n\x00") || !validAPIKey(request.GitToken) {
			return ErrInvalidTarget
		}
		if _, _, err := gitidentity.Normalize(request.GitCommitName, request.GitCommitEmail); err != nil {
			return ErrInvalidTarget
		}
	}
	return nil
}

func normalizeHostTarget(target HostTarget) (HostTarget, error) {
	target.Host = strings.TrimSpace(target.Host)
	if target.Port == 0 {
		target.Port = 22
	}
	if !validHost(target.Host) || target.Port < 1 || target.Port > 65535 {
		return HostTarget{}, ErrInvalidTarget
	}
	return target, nil
}

func normalizeTarget(target Target) (Target, error) {
	host, err := normalizeHostTarget(HostTarget{Host: target.Host, Port: target.Port})
	if err != nil {
		return Target{}, err
	}
	target.Host = host.Host
	target.Port = host.Port
	target.User = strings.TrimSpace(target.User)
	target.AuthMethod = strings.TrimSpace(target.AuthMethod)
	if !validUser(target.User) {
		return Target{}, ErrInvalidTarget
	}
	switch target.AuthMethod {
	case "password":
		if target.Password == "" || len(target.Password) > 4096 {
			return Target{}, ErrInvalidTarget
		}
	case "private_key":
		if strings.TrimSpace(target.PrivateKey) == "" || len(target.PrivateKey) > 256<<10 || len(target.PrivateKeyPassphrase) > 4096 {
			return Target{}, ErrInvalidTarget
		}
	default:
		return Target{}, ErrInvalidTarget
	}
	return target, nil
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, part := range strings.Split(host, ".") {
		if part == "" || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func validUser(user string) bool {
	if user == "" || len(user) > 64 {
		return false
	}
	for _, char := range user {
		if char <= 32 || char == 127 {
			return false
		}
	}
	return true
}

func connect(ctx context.Context, target Target, expectedFingerprint string) (*ssh.Client, hostKeyInfo, error) {
	auth, err := authentication(target)
	if err != nil {
		return nil, hostKeyInfo{}, err
	}
	var keyInfo hostKeyInfo
	mismatch := false
	configuration := &ssh.ClientConfig{
		User:    target.User,
		Auth:    []ssh.AuthMethod{auth},
		Timeout: 12 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			keyInfo = hostKeyInfo{Fingerprint: ssh.FingerprintSHA256(key), Type: key.Type()}
			if expectedFingerprint != "" && subtle.ConstantTimeCompare([]byte(keyInfo.Fingerprint), []byte(expectedFingerprint)) != 1 {
				mismatch = true
				return ErrHostKeyMismatch
			}
			return nil
		},
	}
	address := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	connection, err := (&net.Dialer{Timeout: 12 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, hostKeyInfo{}, fmt.Errorf("%w: %v", ErrConnection, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	if value, ok := ctx.Deadline(); ok {
		deadline = value
	}
	_ = connection.SetDeadline(deadline)
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, configuration)
	if err != nil {
		connection.Close()
		if mismatch {
			return nil, hostKeyInfo{}, ErrHostKeyMismatch
		}
		return nil, hostKeyInfo{}, classifyHandshakeError(err)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = clientConnection.Close()
		return nil, hostKeyInfo{}, fmt.Errorf("%w: could not clear SSH handshake deadline: %v", ErrConnection, err)
	}
	return ssh.NewClient(clientConnection, channels, requests), keyInfo, nil
}

func classifyHandshakeError(err error) error {
	var authenticationError *ssh.ServerAuthError
	if errors.As(err, &authenticationError) {
		return fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	return fmt.Errorf("%w: %v", ErrConnection, err)
}

var errHostKeyCaptured = errors.New("SSH host key captured")

func probeHostKey(ctx context.Context, target HostTarget) (HostKeyResult, error) {
	address := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	connection, err := (&net.Dialer{Timeout: 12 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return HostKeyResult{}, fmt.Errorf("%w: %v", ErrConnection, err)
	}
	defer connection.Close()
	deadline := time.Now().Add(15 * time.Second)
	if value, ok := ctx.Deadline(); ok {
		deadline = value
	}
	_ = connection.SetDeadline(deadline)
	var result HostKeyResult
	configuration := &ssh.ClientConfig{
		User:    "wio-host-key-probe",
		Timeout: 12 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			result = HostKeyResult{Fingerprint: ssh.FingerprintSHA256(key), KeyType: key.Type()}
			return errHostKeyCaptured
		},
	}
	_, _, _, err = ssh.NewClientConn(connection, address, configuration)
	if result.Fingerprint != "" && errors.Is(err, errHostKeyCaptured) {
		return result, nil
	}
	if result.Fingerprint != "" {
		return result, nil
	}
	return HostKeyResult{}, fmt.Errorf("%w: %v", ErrConnection, err)
}

func authentication(target Target) (ssh.AuthMethod, error) {
	if target.AuthMethod == "password" {
		return ssh.Password(target.Password), nil
	}
	key := []byte(target.PrivateKey)
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil && target.PrivateKeyPassphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(target.PrivateKeyPassphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("%w: private key could not be parsed", ErrInvalidTarget)
	}
	return ssh.PublicKeys(signer), nil
}

type hostKeyInfo struct {
	Fingerprint string
	Type        string
}

type inspection struct {
	Hostname     string
	OS           string
	Architecture string
	SudoReady    bool
	DockerReady  bool
	GitReady     bool
	CodexReady   bool
	NPMReady     bool
}

func inspect(client *ssh.Client, _ hostKeyInfo) (inspection, error) {
	hostname, err := runTrimmed(client, "hostname")
	if err != nil {
		return inspection{}, fmt.Errorf("%w: could not inspect hostname", ErrConnection)
	}
	osName, err := runTrimmed(client, "uname -s")
	if err != nil {
		return inspection{}, fmt.Errorf("%w: could not inspect operating system", ErrConnection)
	}
	architecture, err := runTrimmed(client, "uname -m")
	if err != nil {
		return inspection{}, fmt.Errorf("%w: could not inspect architecture", ErrConnection)
	}
	uid, _ := runTrimmed(client, "id -u")
	root := uid == "0"
	sudoReady := root
	if !root {
		_, sudoErr := run(client, "sudo -n true")
		sudoReady = sudoErr == nil
	}
	return inspection{
		Hostname:     strings.TrimSpace(hostname),
		OS:           strings.ToLower(strings.TrimSpace(osName)),
		Architecture: normalizedArchitecture(architecture),
		SudoReady:    sudoReady,
		DockerReady:  commandAvailable(client, "docker"),
		GitReady:     commandAvailable(client, "git"),
		CodexReady:   commandAvailable(client, "codex"),
		NPMReady:     commandAvailable(client, "npm"),
	}, nil
}

func normalizedArchitecture(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func agentAsset(architecture string) (string, error) {
	switch normalizedArchitecture(architecture) {
	case "amd64":
		return "wio-agent-linux-amd64", nil
	case "arm64":
		return "wio-agent-linux-arm64", nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedPlatform, architecture)
	}
}

func isRoot(client *ssh.Client) bool {
	uid, err := runTrimmed(client, "id -u")
	return err == nil && uid == "0"
}

func commandAvailable(client *ssh.Client, command string) bool {
	_, err := run(client, "command -v "+command+" >/dev/null 2>&1")
	return err == nil
}

func upload(client *ssh.Client, root bool, destination, mode string, content []byte, progress func(int64, int64)) error {
	allowed := map[string]string{
		"/usr/local/bin/wio-agent":              "0755",
		"/etc/systemd/system/wio-agent.service": "0644",
		"/etc/wio-agent/codex.key":              "0600",
		"/var/lib/wio-agent/.codex/config.toml": "0600",
		"/var/lib/wio-agent/.git-credentials":   "0600",
		"/var/lib/wio-agent/.gitconfig":         "0600",
	}
	if allowed[destination] != mode {
		return ErrInstallation
	}
	temporary := fmt.Sprintf(`set -eu
umask 077
tmp=%s
trap 'rm -f "$tmp"' EXIT
mkdir -p %s
cat > "$tmp"
chmod %s "$tmp"
mv -f "$tmp" %s
`, shellQuote(destination+".wio-new"), shellQuote(filepath.Dir(destination)), mode, shellQuote(destination))
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	output := &limitedBuffer{remaining: 4 << 10}
	session.Stdout = output
	session.Stderr = output
	reader := io.Reader(bytes.NewReader(content))
	if progress != nil {
		reader = &progressReader{reader: reader, total: int64(len(content)), notify: progress, lastAt: time.Now()}
	}
	session.Stdin = reader
	if err := session.Run(elevated(root, temporary)); err != nil {
		return fmt.Errorf("%w: %s", ErrInstallation, remoteErrorDetail(output.String(), err))
	}
	return nil
}

type progressReader struct {
	reader      io.Reader
	total       int64
	current     int64
	lastCurrent int64
	lastAt      time.Time
	notify      func(int64, int64)
}

func (reader *progressReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.current += int64(count)
	if reader.current == reader.total || reader.current-reader.lastCurrent >= 256<<10 || time.Since(reader.lastAt) >= time.Second {
		reader.notify(reader.current, reader.total)
		reader.lastCurrent = reader.current
		reader.lastAt = time.Now()
	}
	return count, err
}

func remoteErrorDetail(output string, err error) string {
	detail := strings.Join(strings.Fields(output), " ")
	if detail == "" {
		detail = err.Error()
	}
	if len(detail) > 512 {
		detail = detail[:512]
	}
	return detail
}

func elevated(root bool, script string) string {
	if root {
		return "sh -c " + shellQuote(script)
	}
	return "sudo -n sh -c " + shellQuote(script)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func validAPIURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validAPIKey(value string) bool {
	if len(value) < 8 || len(value) > 16<<10 {
		return false
	}
	for _, char := range value {
		if char == 0 || char == '\r' || char == '\n' {
			return false
		}
	}
	return true
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func validModel(value string) bool {
	return modelPattern.MatchString(strings.TrimSpace(value))
}

func codexConfiguration(apiURL, model string) string {
	// Codex omits the entire reasoning object for custom model names unless this
	// capability is forced on, even when turn/start contains an explicit effort.
	return fmt.Sprintf("model = %s\nmodel_provider = \"wio_api\"\nmodel_supports_reasoning_summaries = true\n\n[model_providers.wio_api]\nname = \"Wio API\"\nbase_url = %s\nenv_key = \"WIO_CODEX_API_KEY\"\nwire_api = \"responses\"\n", strconv.Quote(strings.TrimSpace(model)), strconv.Quote(strings.TrimRight(strings.TrimSpace(apiURL), "/")))
}

func gitCredential(endpoint, username, token string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(endpoint), "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidTarget
	}
	parsed.User = url.UserPassword(strings.TrimSpace(username), token)
	return parsed.String(), nil
}

func runTrimmed(client *ssh.Client, command string) (string, error) {
	output, err := run(client, command)
	return strings.TrimSpace(output), err
}

func run(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	output := &limitedBuffer{remaining: 64 << 10}
	session.Stdout = output
	session.Stderr = output
	err = session.Run(command)
	return output.String(), err
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	if b.remaining > 0 {
		if len(value) > b.remaining {
			value = value[:b.remaining]
		}
		_, _ = b.buffer.Write(value)
		b.remaining -= len(value)
	}
	return original, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

var enrolledServerPattern = regexp.MustCompile(`Enrolled server ([0-9a-fA-F-]+);`)

func enrollmentServerID(output string) string {
	match := enrolledServerPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}
