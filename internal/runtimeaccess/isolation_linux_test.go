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
			if strings.Contains(strings.Join(args, " "), "--simulate") {
				return "Inst bubblewrap (0.11.0-1 Debian:13/stable [amd64])\nConf bubblewrap (0.11.0-1 Debian:13/stable [amd64])\n", nil
			}
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
		if strings.HasPrefix(call, "apt-get") && !strings.Contains(call, "--simulate") {
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
		if strings.HasPrefix(call, "apt-get") && !strings.Contains(call, "--simulate") {
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
		if strings.Contains(call, " install ") && !strings.Contains(call, "--simulate") {
			t.Fatal("installed an unconfirmed candidate")
		}
	}
}

func TestIsolationInstallReportsRealFailureWithoutMirrorCredentials(t *testing.T) {
	e, _ := environmentFixture(t)
	original := e.Run
	e.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "apt-get" && !strings.Contains(strings.Join(args, " "), "--simulate") {
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

func TestUnsupportedKernelNeverOffersKernelOrBackportsInstallation(t *testing.T) {
	e, calls := environmentFixture(t)
	e.ABI = func() (int, error) { return 7, errors.New("ABI too old") }
	p := e.Plan(context.Background(), "revision")
	if p.CanInstall || p.RequiresReboot || p.Source != "" || len(p.Packages) != 0 || p.State != "blocked" {
		t.Fatalf("reboot plan offered: %+v", p)
	}
	r := e.Install(context.Background(), "revision", p.PlanID)
	if r.State != "failed" {
		t.Fatal("unsupported kernel install accepted")
	}
	for _, call := range *calls {
		if strings.HasPrefix(call, "apt-") {
			t.Fatalf("kernel repair touched APT: %s", call)
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
		if strings.HasPrefix(call, "apt-get") && !strings.Contains(call, "--simulate") {
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
	if _, err := h.Handle(ctx, Request{Action: "isolation-install", ServerID: "another-node", PlanID: p.IsolationEnvironment.PlanID, UserspaceOnly: true}); err == nil {
		t.Fatal("different server accepted")
	}
	r, err := h.Handle(ctx, Request{Action: "isolation-install", ServerID: "node", PlanID: p.IsolationEnvironment.PlanID, UserspaceOnly: true})
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

func TestUserspaceInstallRejectsSystemDependenciesAndRebootMarkers(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		marker       bool
	}{
		{"kernel", "Inst bubblewrap (0.11.0-1 Debian [amd64])\nInst linux-image-amd64 (7.2 Debian [amd64])", false},
		{"libc upgrade", "Inst libc6 (2.42 Debian [amd64])\nInst bubblewrap (0.11.0-1 Debian [amd64])", false},
		{"pending kernel configuration", "Inst bubblewrap (0.11.0-1 Debian [amd64])\nConf linux-image-amd64 (7.2 Debian [amd64])", false},
		{"removal", "Remv systemd [257]\nInst bubblewrap (0.11.0-1 Debian [amd64])", false},
		{"truncated", strings.Repeat("x", 4096), false},
		{"reboot marker", "Inst bubblewrap (0.11.0-1 Debian [amd64])", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, calls := environmentFixture(t)
			original := e.Run
			e.Run = func(ctx context.Context, name string, args ...string) (string, error) {
				if name == "apt-get" && strings.Contains(strings.Join(args, " "), "--simulate") {
					return tc.output, nil
				}
				return original(ctx, name, args...)
			}
			if tc.marker {
				if err := os.MkdirAll(filepath.Join(e.Root, "run"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(e.Root, "run/reboot-required"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			p := e.Plan(context.Background(), "revision")
			if p.CanInstall || p.RequiresReboot || p.Error == "" {
				t.Fatalf("unsafe plan offered: %+v", p)
			}
			if e.Install(context.Background(), "revision", p.PlanID).State != "failed" {
				t.Fatal("unsafe install accepted")
			}
			for _, call := range *calls {
				if strings.Contains(call, "--assume-yes") {
					t.Fatal("unsafe package installation")
				}
			}
		})
	}
}

func TestHelperRejectsLegacyInstallWithoutUserspaceApproval(t *testing.T) {
	h, _ := installed(t)
	e, calls := environmentFixture(t)
	h.Environment = &e
	p := e.Plan(context.Background(), "revision")
	for _, request := range []Request{{Action: "isolation-install", ServerID: "node", PlanID: p.PlanID}, {Action: "isolation-install", ServerID: "node", PlanID: p.PlanID, UserspaceOnly: true, Source: "debian-backports"}} {
		if _, err := h.Handle(context.Background(), request); err == nil {
			t.Fatal("legacy reboot-capable installation accepted")
		}
	}
	for _, call := range *calls {
		if strings.Contains(call, "--assume-yes") {
			t.Fatal("legacy request installed packages")
		}
	}
}
