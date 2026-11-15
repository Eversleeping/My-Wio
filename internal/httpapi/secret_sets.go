package httpapi

import (
	"github.com/go-chi/chi/v5"
	"net/http"
	"strings"
)

func (a *API) secretSets(w http.ResponseWriter, r *http.Request) {
	sets, err := a.store.ListSecretSets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list secret sets")
		return
	}
	writeJSON(w, http.StatusOK, sets)
}

func (a *API) upsertSecretSet(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Values map[string]string `json:"values"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Values) == 0 {
		writeError(w, http.StatusBadRequest, "name and values are required")
		return
	}
	ciphertext, err := a.vault.Encrypt(input.Values)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not encrypt secret set")
		return
	}
	id, err := a.store.UpsertSecretSet(r.Context(), input.ID, input.Name, ciphertext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save secret set")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "vault.secret_set.write", "secret_set", id, map[string]any{"name": input.Name, "keys": mapKeys(input.Values)}, clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": input.Name})
}

func (a *API) deleteSecretSet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "secretID")
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Q("DELETE FROM secret_sets WHERE id=?"), id)
	if err != nil {
		writeStoreError(w, err, "could not delete secret set")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "secret set not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
