package runtimeaccess

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func installed(t *testing.T) (Helper, []byte) {
	t.Helper()
	h := Helper{Root: t.TempDir(), Run: func(context.Context, string, ...string) error { return nil }}
	raw := []byte(`{"server_id":"node","agent_token":"synthetic-private","allow_sudo":false,"unknown":{"keep":true},"session_isolation":{"enabled":false,"uid":1234,"runtime_paths":["/custom"]}}`)
	for name, data := range map[string][]byte{configFile: raw, "/etc/systemd/system/wio-agent.service": []byte("[Service]\n"), overrideFile: []byte("old override\n")} {
		if err := os.MkdirAll(filepath.Dir(h.path(name)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.path(name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return h, raw
}
func TestRuntimeAccessPreservesConfigurationAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"sudo", "isolation", "reload-fails", "restart-fails"} {
		t.Run(scenario, func(t *testing.T) {
			h, original := installed(t)
			ctx := context.Background()
			current, err := h.Handle(ctx, Request{Action: "read", ServerID: "node"})
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(current)
			if strings.Contains(string(encoded), "synthetic-private") {
				t.Fatal("secret in settings reply")
			}
			current.AllowSudo = scenario != "isolation"
			current.SessionIsolation = scenario == "isolation"
			failed := false
			h.Run = func(_ context.Context, name string, args ...string) error {
				if name == "systemctl" && len(args) > 0 && !failed && (scenario == "reload-fails" && args[0] == "daemon-reload" || scenario == "restart-fails" && args[0] == "restart") {
					failed = true
					return errors.New("synthetic failure")
				}
				return nil
			}
			saved, err := h.Handle(ctx, Request{Action: "apply", ServerID: "node", Settings: &current})
			if scenario == "reload-fails" {
				if err == nil {
					t.Fatal("failed reload reported success")
				}
			} else {
				if err != nil || !saved.RestartRequired {
					t.Fatalf("apply: %+v %v", saved, err)
				}
				// Staging must not restart the service or lose identity/advanced fields.
				raw, _ := os.ReadFile(h.path(configFile))
				if !strings.Contains(string(raw), "synthetic-private") || !strings.Contains(string(raw), "1234") || !strings.Contains(string(raw), "/custom") || !strings.Contains(string(raw), "unknown") {
					t.Fatal("advanced configuration lost")
				}
				// A repeat after reconnect must preserve the original rollback snapshot.
				same := saved
				if _, err := h.Handle(ctx, Request{Action: "apply", ServerID: "node", Settings: &same}); err != nil {
					t.Fatal(err)
				}
				_, err = h.Handle(ctx, Request{Action: "restart", ServerID: "node", Revision: saved.Revision})
				if (err != nil) != (scenario == "restart-fails") {
					t.Fatalf("restart: %v", err)
				}
			}
			if strings.HasSuffix(scenario, "fails") {
				got, _ := os.ReadFile(h.path(configFile))
				override, _ := os.ReadFile(h.path(overrideFile))
				if string(got) != string(original) || string(override) != "old override\n" {
					t.Fatal("rollback did not restore exact files")
				}
				if _, err := os.Stat(h.path(sudoFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("sudo policy survived rollback")
				}
			} else {
				got, err := h.Handle(ctx, Request{Action: "read", ServerID: "node"})
				if err != nil || got.AllowSudo != current.AllowSudo || got.SessionIsolation != current.SessionIsolation {
					t.Fatalf("readback: %+v %v", got, err)
				}
			}
			if _, err := os.Stat(h.path(backupFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed change left secret backup")
			}
		})
	}
}
func TestRuntimeAccessRejectsStaleConflictIdentityAndUnknownActions(t *testing.T) {
	h, raw := installed(t)
	ctx := context.Background()
	current, _ := h.Handle(ctx, Request{Action: "read", ServerID: "node"})
	for _, r := range []Request{
		{Action: "read", ServerID: "other"}, {Action: "shell", ServerID: "node"},
		{Action: "apply", ServerID: "node", Settings: &protocol.AgentAccessSettings{AllowSudo: true, Revision: strings.Repeat("0", 64)}},
		{Action: "apply", ServerID: "node", Settings: &protocol.AgentAccessSettings{AllowSudo: true, SessionIsolation: true, Revision: current.Revision}},
		{Action: "restart", ServerID: "node", Revision: strings.Repeat("0", 64)},
	} {
		if _, err := h.Handle(ctx, r); err == nil {
			t.Fatalf("accepted invalid request: %+v", r)
		}
	}
	got, _ := os.ReadFile(h.path(configFile))
	if string(got) != string(raw) {
		t.Fatal("invalid request changed configuration")
	}
}
