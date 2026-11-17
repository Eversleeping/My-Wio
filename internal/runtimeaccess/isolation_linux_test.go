package runtimeaccess

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func environmentFixture(t *testing.T) (EnvironmentInstaller, *[]string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/os-release"), []byte("ID=debian\nVERSION_ID=13\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	e := EnvironmentInstaller{Root: root, ABI: func() (int, error) { return 9, nil }}
	e.Run = func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "bwrap":
			return "", errors.New("missing")
		case "apt-cache":
			if args[len(args)-1] != "bubblewrap" {
				return "Installed: (none)\nCandidate: 9.0.0-1\n", nil
			}
			return "Installed: (none)\nCandidate: 0.11.0-1\n", nil
		case "apt-get":
			return "", nil
		case "dpkg-query":
			if args[len(args)-1] != "bubblewrap" {
				return "install ok installed\t9.0.0-1", nil
			}
			return "install ok installed\t0.11.0-1", nil
		case "uname":
			return "6.12.0-test", nil
		}
		t.Fatalf("unexpected program %s", name)
		return "", nil
	}
	return e, &calls
}

func TestIsolationPlanDoesNotInstallAndPinsConfirmation(t *testing.T) {
	e, calls := environmentFixture(t)
	ctx := context.Background()
	p := e.Plan(ctx, strings.Repeat("a", 64))
	if !p.CanInstall || p.State != "install_required" || len(p.Packages) != 1 || p.Packages[0].Name != "bubblewrap" {
		t.Fatalf("plan %+v", p)
	}
	for _, call := range *calls {
		if strings.HasPrefix(call, "apt-get") {
			t.Fatal("inspection changed package state")
		}
	}
	for _, planID := range []string{"", strings.Repeat("f", 64), "$(touch bad)"} {
		result := e.Install(ctx, strings.Repeat("a", 64), planID)
		if result.State != "failed" {
			t.Fatalf("unconfirmed installation: %+v", result)
		}
	}
	for _, call := range *calls {
		if strings.HasPrefix(call, "apt-get") {
			t.Fatal("unconfirmed plan performed installation")
		}
	}
	r := e.Install(ctx, strings.Repeat("a", 64), p.PlanID)
	if r.State != "verify" || r.CanInstall || len(r.Logs) < 2 {
		t.Fatalf("not verified: %+v", r)
	}
	found := false
	for _, call := range *calls {
		if strings.Contains(call, "--no-remove install bubblewrap=0.11.0-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("exact pinned package missing: %v", *calls)
	}
}

func TestIsolationCandidateChangeRequiresNewConfirmation(t *testing.T) {
	e, calls := environmentFixture(t)
	original := e.Run
	refreshed := false
	e.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "apt-get" && args[len(args)-1] == "update" {
			refreshed = true
		}
		if name == "apt-cache" && refreshed {
			return "Installed: (none)\nCandidate: 0.12.0-1\n", nil
		}
		return original(ctx, name, args...)
	}
	p := e.Plan(context.Background(), "revision")
	r := e.Install(context.Background(), "revision", p.PlanID)
	if r.State != "failed" || r.PlanID == p.PlanID {
		t.Fatalf("changed candidate accepted: %+v", r)
	}
	for _, call := range *calls {
		if strings.Contains(call, " install ") {
			t.Fatal("installed an unconfirmed candidate")
		}
	}
}

func TestIsolationInstallReportsRealFailureWithoutMirrorCredentials(t *testing.T) {
	e, _ := environmentFixture(t)
	original := e.Run
	e.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "apt-get" {
			return "https://synthetic-user:synthetic-secret@example.invalid: package lock unavailable", errors.New("failed")
		}
		return original(ctx, name, args...)
	}
	p := e.Plan(context.Background(), "revision")
	r := e.Install(context.Background(), "revision", p.PlanID)
	raw, _ := json.Marshal(r)
	if r.State != "failed" || !strings.Contains(r.Error, "busy") || strings.Contains(string(raw), "synthetic-secret") {
		t.Fatalf("unsafe failure: %s", raw)
	}
}

func TestKernelInstallNeverClaimsIsolationReadyOrReboots(t *testing.T) {
	e, calls := environmentFixture(t)
	e.ABI = func() (int, error) { return 6, errors.New("ABI too old") }
	p := e.Plan(context.Background(), "revision")
	if !p.CanInstall || !p.RequiresReboot || len(p.Packages) != 2 {
		t.Fatalf("kernel not planned: %+v", p)
	}
	r := e.Install(context.Background(), "revision", p.PlanID)
	if r.State != "reboot_required" || !r.RequiresReboot {
		t.Fatalf("kernel installation mislabeled: %+v", r)
	}
	for _, call := range *calls {
		if strings.Contains(call, "reboot") || strings.Contains(call, "systemctl") {
			t.Fatalf("unconfirmed restart: %s", call)
		}
	}
}

func TestContainerKernelCannotBeFixedByInstallingContainerPackages(t *testing.T) {
	e, calls := environmentFixture(t)
	e.ABI = func() (int, error) { return 6, errors.New("ABI too old") }
	if err := os.WriteFile(filepath.Join(e.Root, ".dockerenv"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	p := e.Plan(context.Background(), "revision")
	if p.CanInstall || p.Error == "" {
		t.Fatalf("container installation offered: %+v", p)
	}
	r := e.Install(context.Background(), "revision", p.PlanID)
	if r.State != "failed" {
		t.Fatalf("container installation accepted: %+v", r)
	}
	for _, call := range *calls {
		if strings.HasPrefix(call, "apt-get") {
			t.Fatal("container kernel installation attempted")
		}
	}
}

func TestEnvironmentInstallPreservesRuntimeConfigurationAndRejectsWrongServer(t *testing.T) {
	h, original := installed(t)
	e, _ := environmentFixture(t)
	h.Environment = &e
	ctx := context.Background()
	p, err := h.Handle(ctx, Request{Action: "isolation-plan", ServerID: "node"})
	if err != nil || p.IsolationEnvironment == nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(ctx, Request{Action: "isolation-install", ServerID: "another-node", PlanID: p.IsolationEnvironment.PlanID}); err == nil {
		t.Fatal("different server accepted")
	}
	r, err := h.Handle(ctx, Request{Action: "isolation-install", ServerID: "node", PlanID: p.IsolationEnvironment.PlanID})
	if err != nil || r.IsolationEnvironment.State != "verify" {
		t.Fatalf("install: %+v %v", r, err)
	}
	after, _ := os.ReadFile(h.path(configFile))
	if !reflect.DeepEqual(original, after) {
		t.Fatal("installation modified runtime settings")
	}
	if _, err := os.Stat(h.path(backupFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("installation staged a runtime restart")
	}
}
