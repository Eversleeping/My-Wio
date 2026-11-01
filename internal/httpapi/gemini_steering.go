package httpapi

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

func (a *API) steerGeminiTurn(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Prompt      string               `json:"prompt"`
		OperationID string               `json:"operation_id"`
		RequestID   string               `json:"request_id"`
		Images      []protocol.TurnImage `json:"images"`
	}
	if !decodeJSONLimit(w, r, &input, 6<<20) {
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	_, requestErr := uuid.Parse(input.RequestID)
	if (input.Prompt == "" && len(input.Images) == 0) || len(input.Prompt) > 8192 || input.OperationID == "" || requestErr != nil || protocol.ValidateGeminiImages(input.Images) != nil {
		writeError(w, 400, "invalid Gemini steering instruction")
		return
	}
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if !thread.SteerSupported {
		writeError(w, 409, "Gemini steering requires Agent 0.2.79 and Gemini CLI 0.66.0 or newer")
		return
	}
	if len(input.Images) > 0 && !thread.SteerImagesSupported {
		writeError(w, 409, "running image updates require Agent 0.2.88 and Gemini CLI 0.66.0 or newer")
		return
	}
	event, id, err := a.store.QueueGeminiSteer(r.Context(), thread.ID, input.OperationID, input.RequestID, input.Prompt, input.Images...)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if event.EventID != "" {
		a.hub.Publish(event)
		a.auditGemini(r, "gemini.turn.steer", thread.ID)
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]string{"operation_id": id})
}
