package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sshbootstrap"
)

type serverAccessManager interface {
	Access(context.Context, sshbootstrap.AccessRequest) (sshbootstrap.AccessSettings, error)
}

func (a *API) serverAccess(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapMu.TryLock() {
		writeBootstrapCode(w, 429, "server_operation_busy", "another SSH server operation is already running")
		return
	}
	defer a.bootstrapMu.Unlock()
	serverID := chi.URLParam(r, "serverID")
	server, err := a.store.Server(r.Context(), serverID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "server not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load server")
		return
	}
	if server.IsControlPlane {
		writeError(w, 400, "the built-in Agent's access settings are managed by its deployment configuration")
		return
	}
	var input struct {
		sshConnectionInput
		HostKeyFingerprint string                       `json:"host_key_fingerprint"`
		Settings           *sshbootstrap.AccessSettings `json:"settings"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Settings != nil {
		if input.Settings.AllowSudo && input.Settings.SessionIsolation {
			a.writeBootstrapError(w, sshbootstrap.ErrIsolationSudoConflict)
			return
		}
		if input.Settings.SessionIsolation && !server.Supports(protocol.CapabilitySessionIsolation) {
			writeError(w, 409, "upgrade this server's Agent before enabling session isolation")
			return
		}
		if len(input.Settings.Revision) != 64 {
			writeError(w, 400, "read server settings before applying changes")
			return
		}
		for _, engine := range []string{"codex", "claude", "gemini"} {
			blockers, err := a.store.CLIInstallBlockers(r.Context(), serverID, engine)
			if err != nil {
				writeError(w, 500, "could not check active sessions")
				return
			}
			if blockers.Busy() {
				writeError(w, 409, "finish active sessions and server installations before changing access settings")
				return
			}
		}
	}
	manager, ok := a.bootstrapper.(serverAccessManager)
	if !ok {
		writeError(w, 503, "server access settings are unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	settings, err := manager.Access(ctx, sshbootstrap.AccessRequest{Target: input.target(), ExpectedFingerprint: input.HostKeyFingerprint, ExpectedServerID: serverID, Settings: input.Settings})
	if err != nil {
		if errors.Is(err, sshbootstrap.ErrAccessChanged) {
			writeError(w, 409, err.Error())
			return
		}
		if errors.Is(err, sshbootstrap.ErrAccessSettings) {
			writeError(w, 422, "could not apply access settings; reconnect and read settings to verify the current state")
			return
		}
		a.writeBootstrapError(w, err)
		return
	}
	if input.Settings != nil {
		_ = a.store.Audit(r.Context(), currentSession(r).UserID, "server.access.update", "server", serverID, map[string]bool{"allow_sudo": settings.AllowSudo, "session_isolation": settings.SessionIsolation}, clientIP(r))
	}
	writeJSON(w, 200, settings)
}
