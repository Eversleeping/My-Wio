package httpapi

import (
	"net/http"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
)

func (a *API) steerClaudeTurn(w http.ResponseWriter, r *http.Request) {
	var input protocol.ClaudeSteerCommand
	if !decodeJSONLimit(w, r, &input, 6<<20) {
		return
	}
	thread, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	input.ThreadID = thread.ID
	input.Prompt = strings.TrimSpace(input.Prompt)
	if err := protocol.ValidateClaudeSteer(input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if !thread.SteerSupported {
		writeError(w, 409, "Claude steering requires Agent 0.2.95 and Claude Code 2.1.286 or newer")
		return
	}
	event, id, err := a.store.QueueClaudeSteer(r.Context(), input)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if event.EventID != "" {
		a.hub.Publish(event)
		a.auditClaude(r, "claude.turn.steer", thread.ID)
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]string{"operation_id": id})
}
