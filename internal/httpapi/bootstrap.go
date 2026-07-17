package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/sshbootstrap"
)

type serverBootstrapper interface {
	Probe(context.Context, sshbootstrap.HostTarget) (sshbootstrap.HostKeyResult, error)
	Install(context.Context, sshbootstrap.InstallRequest) (sshbootstrap.InstallResult, error)
}

type sshHostInput struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type sshConnectionInput struct {
	Host                 string `json:"host"`
	Port                 int    `json:"port"`
	User                 string `json:"user"`
	AuthMethod           string `json:"auth_method"`
	Password             string `json:"password"`
	PrivateKey           string `json:"private_key"`
	PrivateKeyPassphrase string `json:"private_key_passphrase"`
}

func (input sshConnectionInput) target() sshbootstrap.Target {
	target := sshbootstrap.Target{
		Host:                 input.Host,
		Port:                 input.Port,
		User:                 input.User,
		AuthMethod:           input.AuthMethod,
		Password:             input.Password,
		PrivateKey:           input.PrivateKey,
		PrivateKeyPassphrase: input.PrivateKeyPassphrase,
	}
	if target.AuthMethod == "password" {
		target.PrivateKey = ""
		target.PrivateKeyPassphrase = ""
	} else if target.AuthMethod == "private_key" {
		target.Password = ""
	}
	return target
}

func (a *API) probeServerSSH(w http.ResponseWriter, r *http.Request) {
	var input sshHostInput
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	result, err := a.bootstrapper.Probe(ctx, sshbootstrap.HostTarget{Host: input.Host, Port: input.Port})
	if err != nil {
		a.writeBootstrapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) bootstrapServerSSH(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapMu.TryLock() {
		writeBootstrapCode(w, http.StatusTooManyRequests, "install_busy", "another server installation is already running")
		return
	}
	defer a.bootstrapMu.Unlock()

	var input struct {
		sshConnectionInput
		Name               string   `json:"name"`
		ScanRoots          []string `json:"scan_roots"`
		HostKeyFingerprint string   `json:"host_key_fingerprint"`
		CodexAPIURL        string   `json:"codex_api_url"`
		CodexAPIKey        string   `json:"codex_api_key"`
		CodexModel         string   `json:"codex_model"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		writeBootstrapCode(w, http.StatusBadRequest, "server_name_required", "server name is required")
		return
	}
	if len(input.ScanRoots) == 0 {
		input.ScanRoots = []string{"/srv", "/opt", "/home"}
	}
	for index, root := range input.ScanRoots {
		root = path.Clean(strings.TrimSpace(root))
		if !path.IsAbs(root) {
			writeBootstrapCode(w, http.StatusBadRequest, "scan_roots_invalid", "scan roots must be absolute Linux paths")
			return
		}
		input.ScanRoots[index] = root
	}

	token, err := security.RandomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create enrollment token")
		return
	}
	expires := time.Now().UTC().Add(15 * time.Minute)
	enrollmentID, err := a.store.CreateEnrollment(r.Context(), input.Name, input.ScanRoots, token, expires)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create enrollment")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 95*time.Second)
	defer cancel()
	result, err := a.bootstrapper.Install(ctx, sshbootstrap.InstallRequest{
		Target:              input.target(),
		ExpectedFingerprint: strings.TrimSpace(input.HostKeyFingerprint),
		ControlURL:          a.agentControlURL(r),
		EnrollmentToken:     token,
		CodexAPIURL:         strings.TrimSpace(input.CodexAPIURL),
		CodexAPIKey:         input.CodexAPIKey,
		CodexModel:          strings.TrimSpace(input.CodexModel),
	})
	if err != nil {
		_ = a.store.DeleteUnusedEnrollment(r.Context(), enrollmentID)
		a.log.Warn("SSH server bootstrap failed", "host", strings.TrimSpace(input.Host), "error", err)
		a.writeBootstrapError(w, err)
		return
	}

	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "server.ssh.bootstrap", "server", result.ServerID, map[string]any{
		"name": input.Name, "host": strings.TrimSpace(input.Host), "port": input.Port, "user": strings.TrimSpace(input.User),
		"architecture": result.Architecture, "scan_roots": input.ScanRoots, "codex_api_url": strings.TrimSpace(input.CodexAPIURL), "codex_model": strings.TrimSpace(input.CodexModel), "warnings": result.Warnings,
	}, clientIP(r))
	writeJSON(w, http.StatusCreated, result)
}

func (a *API) agentControlURL(r *http.Request) string {
	if a.publicURL != "" {
		return a.publicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

func (a *API) writeBootstrapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sshbootstrap.ErrInvalidTarget):
		writeBootstrapCode(w, http.StatusBadRequest, "invalid_configuration", "invalid SSH or Codex API configuration")
	case errors.Is(err, sshbootstrap.ErrHostKeyMismatch):
		writeBootstrapCode(w, http.StatusConflict, "fingerprint_changed", "SSH host key fingerprint changed; probe the server again")
	case errors.Is(err, sshbootstrap.ErrAuthentication):
		writeBootstrapCode(w, http.StatusUnprocessableEntity, "ssh_auth_failed", "the SSH username or credential was rejected")
	case errors.Is(err, sshbootstrap.ErrPrivilegeRequired):
		writeBootstrapCode(w, http.StatusBadRequest, "sudo_required", "the SSH user must be root or have passwordless sudo")
	case errors.Is(err, sshbootstrap.ErrUnsupportedPlatform):
		writeBootstrapCode(w, http.StatusBadRequest, "unsupported_platform", "only Linux amd64 and arm64 servers are supported")
	case errors.Is(err, sshbootstrap.ErrAssetsUnavailable):
		writeBootstrapCode(w, http.StatusServiceUnavailable, "assets_unavailable", "agent installation assets are unavailable")
	case errors.Is(err, sshbootstrap.ErrInstallation):
		writeBootstrapCode(w, http.StatusUnprocessableEntity, "installation_failed", "connected to the server but could not install the agent")
	default:
		writeBootstrapCode(w, http.StatusBadGateway, "connection_failed", "could not connect to or configure the server")
	}
}

func writeBootstrapCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}
