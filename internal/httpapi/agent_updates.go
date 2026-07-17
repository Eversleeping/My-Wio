package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/agentupdate"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) updateAgent(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	server, err := a.store.Server(r.Context(), serverID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	if !buildinfo.SupportsSelfUpdate(server.AgentVersion) {
		writeError(w, http.StatusConflict, "Agent must be reinstalled once before remote updates are supported")
		return
	}
	if !buildinfo.UpdateAvailable(server.AgentVersion, buildinfo.Version) {
		writeError(w, http.StatusConflict, "Agent is already up to date")
		return
	}
	if a.agentUpdates == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent update packages are unavailable")
		return
	}
	command, err := a.agentUpdates.Command()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Agent update packages are unavailable")
		return
	}
	active, err := a.store.HasActiveOperation(r.Context(), server.ID, "agent.update")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check Agent update queue")
		return
	}
	if active {
		writeError(w, http.StatusConflict, "an Agent update is already queued")
		return
	}
	operationID, err := a.store.QueueOperation(r.Context(), server.ID, "agent.update", command, "agent-update:"+server.ID+":"+command.Version+":"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue Agent update")
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "agent.update", "server", server.ID, map[string]string{"operation_id": operationID, "from_version": server.AgentVersion, "to_version": command.Version}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "version": command.Version})
}

func (a *API) downloadAgentUpdate(w http.ResponseWriter, r *http.Request) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authorization) < 8 || !strings.EqualFold(authorization[:7], "Bearer ") {
		writeError(w, http.StatusUnauthorized, "Agent authentication required")
		return
	}
	if _, err := a.store.AuthenticateAgent(r.Context(), strings.TrimSpace(authorization[7:])); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid Agent token")
		return
	}
	if r.URL.Query().Get("version") != buildinfo.Version {
		writeError(w, http.StatusNotFound, "Agent update package not found")
		return
	}
	if a.agentUpdates == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent update packages are unavailable")
		return
	}
	pkg, err := a.agentUpdates.Package(chi.URLParam(r, "architecture"))
	if errors.Is(err, agentupdate.ErrUnavailable) {
		writeError(w, http.StatusNotFound, "Agent update package not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Agent update package")
		return
	}
	file, err := os.Open(pkg.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not open Agent update package")
		return
	}
	defer file.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, pkg.Filename))
	w.Header().Set("X-Checksum-SHA256", pkg.SHA256)
	http.ServeContent(w, r, pkg.Filename, time.Time{}, file)
}
