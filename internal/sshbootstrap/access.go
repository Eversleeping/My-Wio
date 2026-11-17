package sshbootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

var ErrAccessSettings = errors.New("could not apply server access settings")
var ErrAccessChanged = errors.New("server settings changed; read them again before applying")

type AccessSettings struct {
	AllowSudo bool   `json:"allow_sudo"`
	Revision  string `json:"revision"`
}

type AccessRequest struct {
	Target              Target
	ExpectedFingerprint string
	ExpectedServerID    string
	Settings            *AccessSettings
}

func decodeAccess(raw []byte, expectedID string) (AccessSettings, map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return AccessSettings{}, nil, ErrAccessSettings
	}
	var config struct {
		ServerID  string `json:"server_id"`
		AllowSudo bool   `json:"allow_sudo"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return AccessSettings{}, nil, ErrAccessSettings
	}
	if config.ServerID != expectedID {
		return AccessSettings{}, nil, ErrServerMismatch
	}
	digest := sha256.Sum256(raw)
	return AccessSettings{AllowSudo: config.AllowSudo, Revision: hex.EncodeToString(digest[:])}, fields, nil
}

func patchAccess(raw []byte, expectedID string, next AccessSettings) ([]byte, error) {
	current, fields, err := decodeAccess(raw, expectedID)
	if err != nil {
		return nil, err
	}
	if next.Revision == "" || current.Revision != next.Revision {
		return nil, ErrAccessChanged
	}
	fields["allow_sudo"], _ = json.Marshal(next.AllowSudo)
	return json.MarshalIndent(fields, "", "  ")
}

// Access reads the installed configuration over the same verified, privileged
// SSH connection as enrollment. Only the sudo option and a digest leave here.
func (s *Service) Access(ctx context.Context, request AccessRequest) (AccessSettings, error) {
	target, err := normalizeTarget(request.Target)
	if err != nil {
		return AccessSettings{}, err
	}
	if !validServerID(request.ExpectedServerID) || strings.TrimSpace(request.ExpectedFingerprint) == "" {
		return AccessSettings{}, ErrInvalidTarget
	}
	client, _, err := connect(ctx, target, request.ExpectedFingerprint)
	if err != nil {
		return AccessSettings{}, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	defer func() { close(done); _ = client.Close() }()
	root := isRoot(client)
	raw, err := run(client, elevated(root, "cat /etc/wio-agent/config.json"))
	if err != nil {
		return AccessSettings{}, ErrPrivilegeRequired
	}
	current, _, err := decodeAccess([]byte(raw), request.ExpectedServerID)
	if err != nil || request.Settings == nil {
		return current, err
	}
	updated, err := patchAccess([]byte(raw), request.ExpectedServerID, *request.Settings)
	if err != nil {
		return AccessSettings{}, err
	}
	if err = applyAccess(client, root, updated, *request.Settings); err != nil {
		return AccessSettings{}, err
	}
	result, _, err := decodeAccess(updated, request.ExpectedServerID)
	return result, err
}

func applyAccess(client *ssh.Client, root bool, updated []byte, settings AccessSettings) error {
	directory, err := run(client, elevated(root, "umask 077; mktemp -d /etc/wio-agent/access.XXXXXXXX"))
	directory = strings.TrimSpace(directory)
	if err != nil || !strings.HasPrefix(directory, "/etc/wio-agent/access.") || strings.ContainsAny(directory, "\r\n ") {
		return ErrAccessSettings
	}
	defer func() { _, _ = run(client, elevated(root, "rm -rf -- "+shellQuote(directory))) }()
	if err = upload(client, root, directory+"/next.json", "0600", updated, nil); err != nil {
		return ErrAccessSettings
	}
	_, err = run(client, elevated(root, accessApplyScript(directory, settings)))
	if err != nil {
		return fmt.Errorf("%w; previous configuration was restored when possible; reconnect and read settings to verify", ErrAccessSettings)
	}
	return nil
}

func accessApplyScript(directory string, settings AccessSettings) string {
	hardened, protectSystem, protectHome := "true", "strict", "read-only"
	if settings.AllowSudo {
		hardened, protectSystem, protectHome = "false", "false", "false"
	}
	sudo := "rm -f /etc/sudoers.d/wio-agent"
	if settings.AllowSudo {
		sudo = `command -v sudo >/dev/null
install -d -m 0755 /etc/sudoers.d
printf '%s\n' 'wio-agent ALL=(ALL:ALL) NOPASSWD: ALL' > /etc/sudoers.d/wio-agent
chmod 0440 /etc/sudoers.d/wio-agent
if command -v visudo >/dev/null 2>&1; then visudo -cf /etc/sudoers.d/wio-agent >/dev/null; fi`
	}
	return `set -eu
exec 9>/run/lock/wio-agent-access.lock
flock -n 9
backup=` + shellQuote(directory) + `
config=/etc/wio-agent/config.json
dropin=/etc/systemd/system/wio-agent.service.d/90-wio-access.conf
test -f /etc/systemd/system/wio-agent.service
actual=$(sha256sum "$config" | cut -d ' ' -f 1)
test "$actual" = ` + shellQuote(settings.Revision) + `
cp -p "$config" "$backup/config.json"
if [ -f /etc/sudoers.d/wio-agent ]; then cp -p /etc/sudoers.d/wio-agent "$backup/sudoers"; fi
if [ -f "$dropin" ]; then cp -p "$dropin" "$backup/dropin"; fi
rollback() {
  result=$?
  trap - EXIT HUP INT TERM
  if [ "$result" -ne 0 ]; then
    cp -p "$backup/config.json" "$config"
    if [ -f "$backup/sudoers" ]; then cp -p "$backup/sudoers" /etc/sudoers.d/wio-agent; else rm -f /etc/sudoers.d/wio-agent; fi
    if [ -f "$backup/dropin" ]; then cp -p "$backup/dropin" "$dropin"; else rm -f "$dropin"; fi
    systemctl daemon-reload
    systemctl restart wio-agent.service
  fi
  exit "$result"
}
trap rollback EXIT
trap 'exit 1' HUP INT TERM
systemctl stop wio-agent.service
chown --reference="$config" "$backup/next.json"
chmod 0600 "$backup/next.json"
mv "$backup/next.json" "$config"
` + sudo + `
install -d -m 0755 /etc/systemd/system/wio-agent.service.d
printf '%s\n' '[Service]' 'NoNewPrivileges=` + hardened + `' 'ProtectSystem=` + protectSystem + `' 'ProtectHome=` + protectHome + `' 'RestrictSUIDSGID=` + hardened + `' > "$dropin"
systemctl daemon-reload
systemctl restart wio-agent.service
systemctl is-active --quiet wio-agent.service`
}
