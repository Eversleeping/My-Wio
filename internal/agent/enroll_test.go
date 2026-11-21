package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestEnrollmentRepairPreservesLocalSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"server_id":"repair","agent_token":"rotated","scan_roots":["/new"]}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	previous := Config{ControlURL: server.URL, ServerID: "repair", AgentToken: "old", StateDir: dir, CloneRoot: filepath.Join(dir, "custom"), ScanRoots: []string{dir}, ExtraEnvironment: []string{"CUSTOM_PROXY"}, AllowSudo: true}
	previous.defaults()
	config, err := Enroll(context.Background(), EnrollmentOptions{ControlURL: server.URL, EnrollmentToken: "one-time", ConfigPath: filepath.Join(dir, "config.json"), PreviousConfig: &previous})
	if err != nil {
		t.Fatal(err)
	}
	if config.AgentToken != "rotated" || config.CloneRoot != previous.CloneRoot || !config.AllowSudo || len(config.ExtraEnvironment) != 1 || config.ScanRoots[0] != dir {
		t.Fatalf("repair discarded settings: %+v", config)
	}
}
