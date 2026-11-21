package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/installer"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

// Metadata and preset references are stored alongside a vault-encrypted
// snapshot. Plaintext credentials are only sent to the bound installation session.
type registrationConfiguration struct {
	Name                  string   `json:"name"`
	ScanRoots             []string `json:"scan_roots"`
	Address               string   `json:"address"`
	Configuration         string   `json:"configuration"`
	Notes                 string   `json:"notes"`
	CodexProfileID        string   `json:"codex_profile_id"`
	GitProfileID          string   `json:"git_profile_id"`
	OS                    string   `json:"os"`
	CredentialsCiphertext string   `json:"credentials_ciphertext,omitempty"`
}

type registrationSnapshot struct {
	Request    installer.InstallRequest
	AgentToken string
}

func (c registrationConfiguration) bootstrap() sshBootstrapInput {
	return sshBootstrapInput{Name: c.Name, ScanRoots: c.ScanRoots, Configuration: c.Configuration, Notes: c.Notes, CodexProfileID: c.CodexProfileID, GitProfileID: c.GitProfileID}
}
func (a *API) createServerRegistration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input registrationConfiguration
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.OS == "" {
		input.OS = "linux"
	}
	if input.OS != "linux" {
		writeBootstrapCode(w, 400, "unsupported_platform", "only Linux amd64 and arm64 servers are supported")
		return
	}
	if input.Name == "" || len(input.Name) > 128 || input.CodexProfileID == "" {
		writeError(w, 400, "name and Codex profile are required")
		return
	}
	if len(input.ScanRoots) == 0 {
		input.ScanRoots = []string{"/srv", "/opt", "/home"}
	}
	if len(input.ScanRoots) > 32 {
		writeError(w, 400, "too many scan roots")
		return
	}
	for i, root := range input.ScanRoots {
		root = strings.TrimSpace(root)
		if !path.IsAbs(root) || strings.ContainsAny(root, "\x00\r\n") || len(root) > 4096 {
			writeBootstrapCode(w, 400, "scan_roots_invalid", "scan roots must be absolute Linux paths")
			return
		}
		input.ScanRoots[i] = path.Clean(root)
	}
	metadata, err := normalizeServerMetadata(input.Address, input.Configuration, input.Notes)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	input.Address, input.Configuration, input.Notes = metadata.Address, metadata.Configuration, metadata.Notes
	resolved, err := a.resolveBootstrapCredentialProfiles(r.Context(), input.bootstrap())
	if err != nil {
		a.writeBootstrapError(w, err)
		return
	}
	snapshot := installer.InstallRequest{CodexAPIURL: resolved.CodexAPIURL, CodexAPIKey: resolved.CodexAPIKey, CodexModel: resolved.CodexModel, GitEndpoint: resolved.GitEndpoint, GitUsername: resolved.GitUsername, GitToken: resolved.GitToken, GitCommitName: resolved.GitCommitName, GitCommitEmail: resolved.GitCommitEmail}
	agentToken, err := security.RandomToken(32)
	if err != nil {
		writeError(w, 500, "could not create Agent credentials")
		return
	}
	input.CredentialsCiphertext, err = a.vault.Encrypt(registrationSnapshot{Request: snapshot, AgentToken: agentToken})
	if err != nil {
		writeError(w, 500, "could not protect registration credentials")
		return
	}
	// Fail before issuing a command if no supported installation artifact exists.
	if a.agentUpdates == nil {
		writeBootstrapCode(w, 503, "assets_unavailable", "Agent installation packages are unavailable")
		return
	}
	if _, err := a.agentUpdates.Command(); err != nil {
		writeBootstrapCode(w, 503, "assets_unavailable", "Agent installation packages are unavailable")
		return
	}
	code, err := security.RandomToken(24)
	if err != nil {
		writeError(w, 500, "could not create registration code")
		return
	}
	raw, _ := json.Marshal(input)
	registration, err := a.store.CreateServerRegistration(r.Context(), input.Name, code, string(raw))
	if err != nil {
		writeError(w, 500, "could not create registration")
		return
	}
	origin := a.agentControlURL(r)
	arguments := " -s -- --url " + registrationShellQuote(origin) + " --registration " + registrationShellQuote(registration.ID)
	command := "curl -fsSL " + registrationShellQuote(origin+"/api/agent/install.sh") + " | (if [ \"$(id -u)\" = 0 ]; then sh" + arguments + "; else sudo sh" + arguments + "; fi)"
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "server.registration.create", "registration", registration.ID, map[string]any{"name": input.Name, "os": "linux"}, clientIP(r))
	a.publishRegistration(registration.ID)
	writeJSON(w, 201, map[string]any{"registration": registration, "command": command, "code": code})
}
func registrationShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
func (a *API) listServerRegistrations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	rows, err := a.store.ListServerRegistrations(r.Context())
	if err != nil {
		writeError(w, 500, "could not list registrations")
		return
	}
	for i := range rows {
		a.refreshRegistration(r, &rows[i])
	}
	writeJSON(w, 200, rows)
}
func (a *API) serverRegistration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	row, err := a.store.ServerRegistration(r.Context(), chi.URLParam(r, "registrationID"))
	if err != nil {
		writeError(w, 404, "registration not found")
		return
	}
	a.refreshRegistration(r, &row)
	writeJSON(w, 200, row)
}
func (a *API) refreshRegistration(r *http.Request, row *store.ServerRegistration) {
	now := time.Now().UTC()
	if row.State == "pending" && !row.ExpiresAt.After(now) {
		row.State = "expired"
		return
	}
	if row.State == "waiting_online" && row.ServerID != "" {
		server, err := a.store.Server(r.Context(), row.ServerID)
		if err == nil && server.Status == "online" && server.LastSeenAt != nil && server.LastSeenAt.After(row.CreatedAt) {
			if a.store.CompleteServerRegistration(r.Context(), row.ID) == nil {
				row.State = "complete"
				a.publishRegistration(row.ID)
			}
			return
		}
	}
	if row.State != "complete" && row.State != "revoked" && row.DeviceHash != "" && !row.SessionExpiresAt.After(now) {
		row.State = "expired"
	}
}
func (a *API) revokeServerRegistration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "registrationID")
	if err := a.store.RevokeServerRegistration(r.Context(), id); err != nil {
		writeError(w, 409, "registration cannot be revoked")
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "server.registration.revoke", "registration", id, nil, clientIP(r))
	a.publishRegistration(chi.URLParam(r, "registrationID"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *API) claimServerRegistration(w http.ResponseWriter, r *http.Request) {
	success, transientFailure := false, false
	if a.registrationClaims != nil {
		if _, allowed := a.registrationClaims.reserve(clientIP(r)); !allowed {
			w.Header().Set("Retry-After", "300")
			writeError(w, 429, "too many rejected registration attempts")
			return
		}
		defer func() {
			if transientFailure {
				a.registrationClaims.release(clientIP(r))
			} else if success {
				a.registrationClaims.succeed(clientIP(r))
			} else {
				a.registrationClaims.fail(clientIP(r))
			}
		}()
	}
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		Code         string `json:"code"`
		DeviceKey    string `json:"device_key"`
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.OS != "linux" || input.Architecture != "amd64" && input.Architecture != "arm64" {
		writeBootstrapCode(w, 400, "unsupported_platform", "only Linux amd64 and arm64 servers are supported")
		return
	}
	if len(input.Code) > 256 || len(input.DeviceKey) < 32 || len(input.DeviceKey) > 256 {
		writeError(w, 401, "registration credentials rejected")
		return
	}
	row, err := a.store.ClaimServerRegistration(r.Context(), chi.URLParam(r, "registrationID"), input.Code, input.DeviceKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 401, "registration credentials rejected or expired")
		} else {
			transientFailure = true
			writeError(w, 503, "registration storage is temporarily unavailable")
		}
		return
	}
	success = true
	a.publishRegistration(row.ID)
	a.writeRegistrationPlan(w, r, row, input.DeviceKey)
}
func (a *API) installationSession(w http.ResponseWriter, r *http.Request) {
	row, key, ok := a.authenticateInstallation(w, r)
	if !ok {
		return
	}
	a.writeRegistrationPlan(w, r, row, key)
}
func (a *API) authenticateInstallation(w http.ResponseWriter, r *http.Request) (store.ServerRegistration, string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(key) < 32 || len(key) > 256 {
		writeError(w, 401, "installation session rejected")
		return store.ServerRegistration{}, "", false
	}
	row, err := a.store.AuthenticateServerRegistration(r.Context(), chi.URLParam(r, "registrationID"), key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 401, "installation session rejected or expired")
		} else {
			writeError(w, 503, "registration storage is temporarily unavailable")
		}
		return row, "", false
	}
	return row, key, true
}
func (a *API) registrationInput(row store.ServerRegistration) (registrationConfiguration, error) {
	var input registrationConfiguration
	err := json.Unmarshal([]byte(row.ConfigJSON), &input)
	return input, err
}
func (a *API) writeRegistrationPlan(w http.ResponseWriter, r *http.Request, row store.ServerRegistration, key string) {
	a.refreshRegistration(r, &row)
	if row.State == "complete" {
		writeJSON(w, 200, map[string]any{"registration": row})
		return
	}
	input, err := a.registrationInput(row)
	if err != nil {
		writeError(w, 500, "invalid registration configuration")
		return
	}
	var snapshot registrationSnapshot
	if a.vault == nil || a.vault.Decrypt(input.CredentialsCiphertext, &snapshot) != nil {
		writeError(w, 500, "could not read registration credentials")
		return
	}
	request := snapshot.Request
	request.ControlURL = a.agentControlURL(r)
	request.RegistrationID = row.ID
	request.EnrollmentToken = key
	request.ExpectedServerID = row.ServerID
	request.PreserveAccessSettings = row.ServerID != ""

	writeJSON(w, 200, map[string]any{"registration": row, "request": request})
}

func (a *API) enrollRegisteredAgent(w http.ResponseWriter, r *http.Request) {
	row, key, ok := a.authenticateInstallation(w, r)
	if !ok {
		return
	}
	var input struct {
		Hostname string `json:"hostname"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Hostname) == "" || len(input.Hostname) > 255 {
		writeError(w, 400, "hostname is required")
		return
	}
	configuration, err := a.registrationInput(row)
	if err != nil {
		writeError(w, 500, "invalid registration configuration")
		return
	}
	var snapshot registrationSnapshot
	if a.vault == nil || a.vault.Decrypt(configuration.CredentialsCiphertext, &snapshot) != nil || snapshot.AgentToken == "" {
		writeError(w, 500, "could not read registration credentials")
		return
	}
	token := snapshot.AgentToken
	roots, _ := json.Marshal(configuration.ScanRoots)
	id, err := a.store.EnrollServerRegistration(r.Context(), row.ID, key, input.Hostname, token, store.Enrollment{ServerName: configuration.Name, ScanRoots: string(roots), Address: configuration.Address, Configuration: configuration.Configuration, Notes: configuration.Notes})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 409, "registration cannot enroll this server")
		} else {
			writeError(w, 503, "registration storage is temporarily unavailable")
		}
		return
	}
	codexID, gitID := configuration.CodexProfileID, configuration.GitProfileID
	// Deleted presets do not invalidate the encrypted installation snapshot.
	for _, reference := range []*string{&codexID, &gitID} {
		if *reference != "" {
			if _, err := a.store.CredentialProfile(r.Context(), *reference); errors.Is(err, sql.ErrNoRows) {
				*reference = ""
			} else if err != nil {
				writeError(w, 500, "could not read credential profile")
				return
			}
		}
	}
	if err := a.store.SetServerCredentialProfiles(r.Context(), id, codexID, gitID); err != nil {
		writeError(w, 500, "could not bind credential profiles")
		return
	}
	a.publishRegistration(row.ID)
	writeJSON(w, 201, map[string]any{"server_id": id, "agent_token": token, "scan_roots": configuration.ScanRoots})
}
func (a *API) installationProgress(w http.ResponseWriter, r *http.Request) {
	row, key, ok := a.authenticateInstallation(w, r)
	if !ok {
		return
	}
	var event installer.Event
	if !decodeJSON(w, r, &event) {
		return
	}
	if row.State == "complete" {
		a.publishRegistration(chi.URLParam(r, "registrationID"))
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	var steps []installer.InstallProgress
	_ = json.Unmarshal(row.ProgressJSON, &steps)
	state, code, result := row.State, "", string(row.ResultJSON)
	switch event.Type {
	case "progress":
		if !installer.ValidProgressStep(event.Step) || event.Current < 0 || event.Total < 0 {
			writeError(w, 400, "invalid installation progress")
			return
		}
		progress := installer.InstallProgress{Step: event.Step, Current: event.Current, Total: event.Total}
		found := false
		for i := range steps {
			if steps[i].Step == event.Step {
				steps[i] = progress
				found = true
				break
			}
		}
		if !found {
			steps = append(steps, progress)
		}
		if row.ServerID == "" {
			state = "installing"
		} else {
			state = "registered"
		}
	case "complete":
		if event.Result == nil || row.ServerID == "" || event.Result.ServerID != row.ServerID || (event.Result.Architecture != "amd64" && event.Result.Architecture != "arm64") || !installer.ValidWarnings(event.Result.Warnings) {
			writeError(w, 400, "invalid installation result")
			return
		}
		// A reported service failure must not become a successful registration.
		state = "waiting_online"
		for _, warning := range event.Result.Warnings {
			if warning == "service_start_failed" || warning == "service_not_active" {
				state = "failed"
				code = "service_start_failed"
			}
		}
		safe := installer.InstallResult{ServerID: row.ServerID, Architecture: event.Result.Architecture, Warnings: event.Result.Warnings}
		raw, _ := json.Marshal(safe)
		result = string(raw)
	case "error":
		state = "failed"
		code = "installation_failed"
	default:
		writeError(w, 400, "invalid installation event")
		return
	}
	progress, _ := json.Marshal(steps)
	if err := a.store.UpdateServerRegistrationProgress(r.Context(), row.ID, key, state, string(progress), result, code); err != nil {
		writeError(w, 409, "installation session is no longer active")
		return
	}
	a.publishRegistration(chi.URLParam(r, "registrationID"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) publishRegistration(id string) {
	if a.hub != nil {
		a.hub.Publish(protocol.StreamEvent{StreamID: "registration:" + id, Kind: protocol.EventServerRegistrationUpdated, OccurredAt: time.Now().UTC()})
	}
}
