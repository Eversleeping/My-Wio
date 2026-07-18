package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/sshbootstrap"
	"github.com/wio-platform/wio/internal/store"
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

type sshBootstrapInput struct {
	sshConnectionInput
	Name               string   `json:"name"`
	ScanRoots          []string `json:"scan_roots"`
	Configuration      string   `json:"configuration"`
	Notes              string   `json:"notes"`
	HostKeyFingerprint string   `json:"host_key_fingerprint"`
	CodexAPIURL        string   `json:"codex_api_url"`
	CodexAPIKey        string   `json:"codex_api_key"`
	CodexModel         string   `json:"codex_model"`
	CodexProfileID     string   `json:"codex_profile_id"`
	GitProfileID       string   `json:"git_profile_id"`
	GitEndpoint        string   `json:"-"`
	GitUsername        string   `json:"-"`
	GitToken           string   `json:"-"`
	CodexProfileName   string   `json:"-"`
	GitProfileName     string   `json:"-"`
}

type bootstrapStreamEvent struct {
	Type    string                      `json:"type"`
	Step    string                      `json:"step,omitempty"`
	Current int64                       `json:"current,omitempty"`
	Total   int64                       `json:"total,omitempty"`
	Code    string                      `json:"code,omitempty"`
	Error   string                      `json:"error,omitempty"`
	Detail  string                      `json:"detail,omitempty"`
	Result  *sshbootstrap.InstallResult `json:"result,omitempty"`
}

var (
	errEnrollmentToken   = errors.New("could not create enrollment token")
	errEnrollmentStore   = errors.New("could not create enrollment")
	errCredentialProfile = errors.New("credential profile is invalid")
)

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

	input, ok := decodeSSHBootstrapInput(w, r)
	if !ok {
		return
	}
	result, err := a.runServerBootstrap(r, input, nil)
	if err != nil {
		a.writeBootstrapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (a *API) streamBootstrapServerSSH(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapMu.TryLock() {
		writeBootstrapCode(w, http.StatusTooManyRequests, "install_busy", "another server installation is already running")
		return
	}
	defer a.bootstrapMu.Unlock()

	input, ok := decodeSSHBootstrapInput(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	encoder := json.NewEncoder(w)
	var streamMu sync.Mutex
	emit := func(event bootstrapStreamEvent) {
		streamMu.Lock()
		defer streamMu.Unlock()
		if err := encoder.Encode(event); err == nil {
			flusher.Flush()
		}
	}
	emit(bootstrapStreamEvent{Type: "progress", Step: "starting"})
	heartbeatDone := make(chan struct{})
	heartbeatTicker := time.NewTicker(15 * time.Second)
	var heartbeatWG sync.WaitGroup
	heartbeatWG.Add(1)
	go func() {
		defer heartbeatWG.Done()
		for {
			select {
			case <-heartbeatTicker.C:
				emit(bootstrapStreamEvent{Type: "heartbeat"})
			case <-heartbeatDone:
				return
			}
		}
	}()
	defer func() {
		heartbeatTicker.Stop()
		close(heartbeatDone)
		heartbeatWG.Wait()
	}()
	result, err := a.runServerBootstrap(r, input, func(progress sshbootstrap.InstallProgress) {
		emit(bootstrapStreamEvent{Type: "progress", Step: progress.Step, Current: progress.Current, Total: progress.Total})
	})
	if err != nil {
		_, code, message := bootstrapErrorInfo(err)
		emit(bootstrapStreamEvent{Type: "error", Code: code, Error: message, Detail: bootstrapSafeDetail(err)})
		return
	}
	emit(bootstrapStreamEvent{Type: "complete", Step: "completed", Result: &result})
}

func decodeSSHBootstrapInput(w http.ResponseWriter, r *http.Request) (sshBootstrapInput, bool) {
	var input sshBootstrapInput
	if !decodeJSON(w, r, &input) {
		return sshBootstrapInput{}, false
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		writeBootstrapCode(w, http.StatusBadRequest, "server_name_required", "server name is required")
		return sshBootstrapInput{}, false
	}
	metadata, err := normalizeServerMetadata(input.Host, input.Configuration, input.Notes)
	if err != nil {
		writeBootstrapCode(w, http.StatusBadRequest, "server_metadata_invalid", err.Error())
		return sshBootstrapInput{}, false
	}
	input.Host = metadata.Address
	input.Configuration = metadata.Configuration
	input.Notes = metadata.Notes
	if len(input.ScanRoots) == 0 {
		input.ScanRoots = []string{"/srv", "/opt", "/home"}
	}
	for index, root := range input.ScanRoots {
		root = path.Clean(strings.TrimSpace(root))
		if !path.IsAbs(root) {
			writeBootstrapCode(w, http.StatusBadRequest, "scan_roots_invalid", "scan roots must be absolute Linux paths")
			return sshBootstrapInput{}, false
		}
		input.ScanRoots[index] = root
	}
	return input, true
}

func (a *API) runServerBootstrap(r *http.Request, input sshBootstrapInput, progress func(sshbootstrap.InstallProgress)) (sshbootstrap.InstallResult, error) {
	var err error
	input, err = a.resolveBootstrapCredentialProfiles(r.Context(), input)
	if err != nil {
		return sshbootstrap.InstallResult{}, err
	}
	token, err := security.RandomToken(24)
	if err != nil {
		return sshbootstrap.InstallResult{}, fmt.Errorf("%w: %v", errEnrollmentToken, err)
	}
	expires := time.Now().UTC().Add(15 * time.Minute)
	enrollmentID, err := a.store.CreateEnrollmentWithMetadata(r.Context(), input.Name, input.ScanRoots, token, expires, store.ServerMetadata{
		Address: input.Host, Configuration: input.Configuration, Notes: input.Notes,
	})
	if err != nil {
		return sshbootstrap.InstallResult{}, fmt.Errorf("%w: %v", errEnrollmentStore, err)
	}

	result, err := a.bootstrapper.Install(r.Context(), sshbootstrap.InstallRequest{
		Target:              input.target(),
		ExpectedFingerprint: strings.TrimSpace(input.HostKeyFingerprint),
		ControlURL:          a.agentControlURL(r),
		EnrollmentToken:     token,
		CodexAPIURL:         strings.TrimSpace(input.CodexAPIURL),
		CodexAPIKey:         input.CodexAPIKey,
		CodexModel:          strings.TrimSpace(input.CodexModel),
		GitEndpoint:         input.GitEndpoint,
		GitUsername:         input.GitUsername,
		GitToken:            input.GitToken,
		Progress:            progress,
	})
	if err != nil {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = a.store.DeleteUnusedEnrollment(cleanupContext, enrollmentID)
		cleanupCancel()
		a.log.Warn("SSH server bootstrap failed", "host", strings.TrimSpace(input.Host), "error", err)
		return sshbootstrap.InstallResult{}, err
	}

	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "server.ssh.bootstrap", "server", result.ServerID, map[string]any{
		"name": input.Name, "host": strings.TrimSpace(input.Host), "port": input.Port, "user": strings.TrimSpace(input.User),
		"architecture": result.Architecture, "scan_roots": input.ScanRoots, "codex_api_url": strings.TrimSpace(input.CodexAPIURL), "codex_model": strings.TrimSpace(input.CodexModel),
		"codex_profile_id": input.CodexProfileID, "codex_profile_name": input.CodexProfileName, "git_profile_id": input.GitProfileID, "git_profile_name": input.GitProfileName, "warnings": result.Warnings,
	}, clientIP(r))
	return result, nil
}

func (a *API) resolveBootstrapCredentialProfiles(ctx context.Context, input sshBootstrapInput) (sshBootstrapInput, error) {
	input.CodexProfileID = strings.TrimSpace(input.CodexProfileID)
	input.GitProfileID = strings.TrimSpace(input.GitProfileID)
	if input.CodexProfileID != "" {
		profile, err := a.store.CredentialProfile(ctx, input.CodexProfileID)
		if err != nil || profile.Kind != "codex" || a.vault == nil {
			return sshBootstrapInput{}, errCredentialProfile
		}
		var secret string
		if err := a.vault.Decrypt(profile.Ciphertext, &secret); err != nil {
			return sshBootstrapInput{}, errCredentialProfile
		}
		input.CodexAPIURL = profile.Endpoint
		input.CodexAPIKey = secret
		input.CodexModel = profile.Model
		input.CodexProfileName = profile.Name
	}
	if input.GitProfileID != "" {
		profile, err := a.store.CredentialProfile(ctx, input.GitProfileID)
		if err != nil || profile.Kind != "git" || a.vault == nil {
			return sshBootstrapInput{}, errCredentialProfile
		}
		var secret string
		if err := a.vault.Decrypt(profile.Ciphertext, &secret); err != nil {
			return sshBootstrapInput{}, errCredentialProfile
		}
		input.GitEndpoint = profile.Endpoint
		input.GitUsername = profile.Username
		input.GitToken = secret
		input.GitProfileName = profile.Name
	}
	return input, nil
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
	status, code, message := bootstrapErrorInfo(err)
	writeBootstrapCode(w, status, code, message)
}

func bootstrapErrorInfo(err error) (int, string, string) {
	switch {
	case errors.Is(err, errCredentialProfile):
		return http.StatusBadRequest, "credential_profile_invalid", "the selected credential profile is unavailable or invalid"
	case errors.Is(err, errEnrollmentToken):
		return http.StatusInternalServerError, "enrollment_token_failed", "could not create enrollment token"
	case errors.Is(err, errEnrollmentStore):
		return http.StatusInternalServerError, "enrollment_store_failed", "could not create enrollment"
	case errors.Is(err, sshbootstrap.ErrInvalidTarget):
		return http.StatusBadRequest, "invalid_configuration", "invalid SSH or Codex API configuration"
	case errors.Is(err, sshbootstrap.ErrHostKeyMismatch):
		return http.StatusConflict, "fingerprint_changed", "SSH host key fingerprint changed; probe the server again"
	case errors.Is(err, sshbootstrap.ErrAuthentication):
		return http.StatusUnprocessableEntity, "ssh_auth_failed", "the SSH username or credential was rejected"
	case errors.Is(err, sshbootstrap.ErrPrivilegeRequired):
		return http.StatusBadRequest, "sudo_required", "the SSH user must be root or have passwordless sudo"
	case errors.Is(err, sshbootstrap.ErrUnsupportedPlatform):
		return http.StatusBadRequest, "unsupported_platform", "only Linux amd64 and arm64 servers are supported"
	case errors.Is(err, sshbootstrap.ErrAssetsUnavailable):
		return http.StatusServiceUnavailable, "assets_unavailable", "agent installation assets are unavailable"
	case errors.Is(err, sshbootstrap.ErrInstallation):
		return http.StatusUnprocessableEntity, "installation_failed", "connected to the server but could not install the agent"
	default:
		return http.StatusBadGateway, "connection_failed", "could not connect to or configure the server"
	}
}

func bootstrapSafeDetail(err error) string {
	if !errors.Is(err, sshbootstrap.ErrInstallation) {
		return ""
	}
	detail := strings.Join(strings.Fields(err.Error()), " ")
	if len(detail) > 512 {
		detail = detail[:512]
	}
	return detail
}

func writeBootstrapCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}
