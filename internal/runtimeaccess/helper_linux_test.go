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

func TestRuntimeAccessRecoversAfterApplyAndRollbackReloadBothFail(t *testing.T) {
	h, original := installed(t)
	ctx := context.Background()
	current, _ := h.Handle(ctx, Request{Action: "read", ServerID: "node"})
	next := current
	next.SessionIsolation = true
	h.Run = func(context.Context, string, ...string) error { return errors.New("system bus unavailable") }
	if _, err := h.Handle(ctx, Request{Action: "apply", ServerID: "node", Settings: &next}); err == nil {
		t.Fatal("failed apply reported success")
	}
	raw, _ := os.ReadFile(h.path(configFile))
	if string(raw) != string(original) {
		t.Fatal("failed apply did not restore configuration")
	}
	if _, err := os.Stat(h.path(backupFile)); err != nil {
		t.Fatal("failed rollback reload must retain recovery data")
	}
	if _, err := h.Handle(ctx, Request{Action: "apply", ServerID: "node", Settings: &next}); err == nil || !strings.Contains(err.Error(), "could not finish restoring") {
		t.Fatalf("unavailable systemd must remain retryable and retain recovery data: %v", err)
	}
	// Restoring systemd access must allow a new attempt, without deleting the
	// recovery file by hand or losing the original rollback configuration.
	h.Run = func(context.Context, string, ...string) error { return nil }
	saved, err := h.Handle(ctx, Request{Action: "apply", ServerID: "node", Settings: &next})
	if err != nil || !saved.RestartRequired || !saved.SessionIsolation {
		t.Fatalf("retry after restored rollback: %+v %v", saved, err)
	}
	if _, err := h.Handle(ctx, Request{Action: "restart", ServerID: "node", Revision: saved.Revision}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeAccessDoesNotDiscardRecoveryAfterUnrelatedFileChange(t *testing.T) {
	h, _ := installed(t)
	ctx := context.Background()
	next, _ := h.Handle(ctx, Request{Action: "read", ServerID: "node"})
	next.SessionIsolation = true
	h.Run = func(context.Context, string, ...string) error { return errors.New("system bus unavailable") }
	_, _ = h.Handle(ctx, Request{Action: "apply", ServerID: "node", Settings: &next})
	const edited = "independent administrator change\n"
	if err := os.WriteFile(h.path(overrideFile), []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	h.Run = func(context.Context, string, ...string) error { return nil }
	if _, err := h.Handle(ctx, Request{Action: "apply", ServerID: "node", Settings: &next}); err == nil {
		t.Fatal("partially restored recovery data was discarded")
	}
	if _, err := os.Stat(h.path(backupFile)); err != nil {
		t.Fatal("recovery snapshot lost")
	}
	raw, _ := os.ReadFile(h.path(overrideFile))
	if string(raw) != edited {
		t.Fatal("independent edit was overwritten")
	}
}

func TestRuntimeAccessReadReportsPendingRestartWithoutChangingFiles(t *testing.T) {
	h, _ := installed(t)
	ctx := context.Background()
	current, _ := h.Handle(ctx, Request{Action: "read", ServerID: "node"})
	current.AllowSudo = true
	saved, err := h.Handle(ctx, Request{Action: "apply", ServerID: "node", Settings: &current})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.Run = func(context.Context, string, ...string) error { calls++; return nil }
	read, err := h.Handle(ctx, Request{Action: "read", ServerID: "node"})
	if err != nil || !read.RestartRequired || read.Revision != saved.Revision || calls != 0 {
		t.Fatalf("pending restart not reported safely: %+v %v calls=%d", read, err, calls)
	}
}

func TestRuntimeAccessExplicitRestartLeavesSettingsUnchanged(t *testing.T) {
	h, original := installed(t)
	ctx := context.Background()
	current, _ := h.Handle(ctx, Request{Action: "read", ServerID: "node"})
	restarts := 0
	h.Run = func(_ context.Context, name string, args ...string) error {
		if name != "systemctl" || strings.Join(args, " ") != "restart wio-agent.service" {
			t.Fatalf("unexpected restart command %s %v", name, args)
		}
		restarts++
		return nil
	}
	if _, err := h.Handle(ctx, Request{Action: "restart", ServerID: "node", Revision: current.Revision}); err != nil || restarts != 0 {
		t.Fatal("a configuration restart without a pending change must be a no-op")
	}
	if _, err := h.Handle(ctx, Request{Action: "restart", ServerID: "node", Revision: current.Revision, Force: true}); err != nil || restarts != 1 {
		t.Fatalf("explicit restart failed: count=%d error=%v", restarts, err)
	}
	raw, _ := os.ReadFile(h.path(configFile))
	if string(raw) != string(original) {
		t.Fatal("explicit restart changed runtime settings")
	}
	if _, err := h.Handle(ctx, Request{Action: "read", ServerID: "node", Force: true}); err == nil {
		t.Fatal("a read must not accept a forced restart")
	}
}
