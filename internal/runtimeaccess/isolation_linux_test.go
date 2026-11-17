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
		if strings.Contains(call, " install ") && !strings.Contains(call, "--simulate") {
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

func backportsFixture(t *testing.T) (EnvironmentInstaller, *[]string, *[]string) {
	t.Helper()
	e, calls := environmentFixture(t)
	for name, body := range map[string]string{"usr/share/keyrings/debian-archive-keyring.gpg": "fixture keyring", "etc/apt/sources.list": "deb https://fixture.invalid/debian trixie main\n", "etc/apt/sources.list.d/vendor.sources": "fixture source"} {
		path := filepath.Join(e.Root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e.ABI = func() (int, error) { return 7, errors.New("ABI too old") }
	original := e.Run
	catalogs := []string{}
	e.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		temporary := false
		for _, arg := range args {
			if strings.HasPrefix(arg, "Dir::Etc::sourcelist=") {
				temporary = true
				path := strings.TrimPrefix(arg, "Dir::Etc::sourcelist=")
				catalogs = append(catalogs, filepath.Dir(path))
				body, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(body), "https://deb.debian.org/debian trixie-backports main") || !strings.Contains(string(body), "signed-by=") || !strings.Contains(string(body), "fixture.invalid") {
					t.Fatalf("unsafe catalog %q %v", body, err)
				}
			}
		}
		if name == "apt-cache" && args[len(args)-1] != "bubblewrap" {
			*calls = append(*calls, name+" "+strings.Join(args, " "))
			if temporary {
				if !strings.Contains(strings.Join(args, " "), "APT::Default-Release=trixie-backports") {
					t.Fatal("backports candidate lost its explicit priority")
				}
				return "Installed: 6.12.0-1\nCandidate: 7.2.12-1~bpo13+1\n", nil
			}
			return "Installed: 6.12.0-1\nCandidate: 6.12.99-1\n", nil
		}
		if name == "dpkg-query" && args[len(args)-1] != "bubblewrap" {
			return "install ok installed\t7.2.12-1~bpo13+1", nil
		}
		return original(ctx, name, args...)
	}
	return e, calls, &catalogs
}

func TestBackportsPlanPreservesSourcesAndRequiresExplicitSourceConfirmation(t *testing.T) {
	e, calls, catalogs := backportsFixture(t)
	before := map[string][]byte{}
	for _, name := range []string{"etc/apt/sources.list", "etc/apt/sources.list.d/vendor.sources"} {
		before[name], _ = os.ReadFile(filepath.Join(e.Root, name))
	}
	p := e.Plan(context.Background(), "revision")
	if !p.CanInstall || !p.RequiresReboot || p.Source != debianBackports || p.Packages[1].Version != "7.2.12-1~bpo13+1" {
		t.Fatalf("no executable kernel plan: %+v", p)
	}
	for _, source := range []string{"", "https://untrusted.invalid", "configured"} {
		r := e.Install(context.Background(), "revision", p.PlanID, source)
		if r.State != "failed" || !strings.Contains(r.Error, "explicitly confirmed") {
			t.Fatalf("source approval bypassed: %+v", r)
		}
	}
	for _, call := range *calls {
		if strings.Contains(call, " install ") && !strings.Contains(call, "--simulate") {
			t.Fatal("unapproved source installed")
		}
	}
	stale := p.PlanID
	p.PlanID = ""
	p.Source = ""
	if fingerprint(p, "revision").PlanID == stale {
		t.Fatal("source excluded from fingerprint")
	}
	r := e.Install(context.Background(), "revision", stale, debianBackports)
	if r.State != "reboot_required" || r.CanInstall || r.Source != debianBackports {
		t.Fatalf("kernel outcome: %+v", r)
	}
	found := false
	for _, call := range *calls {
		if strings.Contains(call, " install ") && !strings.Contains(call, "--simulate") {
			found = true
			if !strings.Contains(call, "Dir::Etc::sourcelist=") || !strings.Contains(call, "-t trixie-backports") || !strings.Contains(call, "linux-image-") || !strings.Contains(call, "=7.2.12-1~bpo13+1") {
				t.Fatalf("unpinned installation: %s", call)
			}
		}
	}
	if !found {
		t.Fatal("no actual package install")
	}
	for name, want := range before {
		after, _ := os.ReadFile(filepath.Join(e.Root, name))
		if !reflect.DeepEqual(after, want) {
			t.Fatal("persistent sources changed")
		}
	}
	for _, path := range *catalogs {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("temporary catalog leaked")
		}
	}
}

func TestBackportsSignatureFailureCannotInstallOrExposeMirrorCredentials(t *testing.T) {
	e, calls, _ := backportsFixture(t)
	original := e.Run
	e.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "apt-get" && args[len(args)-1] == "update" {
			return "https://synthetic:secret@example.invalid is not signed", errors.New("bad signature")
		}
		return original(ctx, name, args...)
	}
	p := e.Plan(context.Background(), "revision")
	if p.CanInstall || !strings.Contains(p.Error, "signature") || strings.Contains(p.Error, "secret") {
		t.Fatalf("unsafe plan: %+v", p)
	}
	r := e.Install(context.Background(), "revision", p.PlanID, debianBackports)
	if r.State != "failed" {
		t.Fatal("signature failure accepted")
	}
	for _, call := range *calls {
		if strings.Contains(call, " install ") && !strings.Contains(call, "--simulate") {
			t.Fatal("unsigned repository installed")
		}
	}
}

func TestKnownInsufficientKernelDoesNotOfferFalseUpgrade(t *testing.T) {
	for _, version := range []string{"6.12.99-1", "6.18.12-1", "7.0.12-1"} {
		if kernelHasRequiredFeatures(version) {
			t.Fatalf("insufficient candidate %s", version)
		}
	}
	for _, version := range []string{"7.1.0", "7.1.0-1", "7.2.12-1~bpo13+1", "8.0.1-1"} {
		if !kernelHasRequiredFeatures(version) {
			t.Fatalf("valid upgrade candidate %s", version)
		}
	}
}

func TestBackportsDependencyFailureIsReturnedBeforeOfferingInstallation(t *testing.T) {
	e, calls, _ := backportsFixture(t)
	original := e.Run
	e.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "apt-get" && strings.Contains(strings.Join(args, " "), "--simulate") {
			return "unmet dependencies: held broken packages", errors.New("unresolvable")
		}
		return original(ctx, name, args...)
	}
	p := e.Plan(context.Background(), "revision")
	if p.CanInstall || !strings.Contains(p.Error, "dependency check failed") || strings.Contains(p.Error, "hostname") {
		t.Fatalf("false executable plan: %+v", p)
	}
	for _, call := range *calls {
		if strings.Contains(call, " install ") && !strings.Contains(call, "--simulate") {
			t.Fatal("dependency check installed packages")
		}
	}
}
