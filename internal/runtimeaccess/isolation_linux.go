package runtimeaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sandbox"
)

type PackageRunner func(context.Context, string, ...string) (string, error)

// EnvironmentInstaller accepts only a plan fingerprint. The package allowlist
// and exact candidate versions are derived again on the target host.
type EnvironmentInstaller struct {
	Root string
	Run  PackageRunner
	ABI  func() (int, error)
}

func packageRun(ctx context.Context, name string, args ...string) (string, error) {
	switch name {
	case "apt-cache", "apt-get", "dpkg-query", "uname", "bwrap":
	default:
		return "", errors.New("unsupported isolation environment program")
	}
	program := "/usr/bin/" + name
	if _, err := os.Stat(program); err != nil {
		return "", err
	}
	cmd := childprocess.CommandContext(ctx, program, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l"}
	var output boundedDiagnostic
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	return output.String(), err
}

var packageVersion = regexp.MustCompile(`^[A-Za-z0-9.+:~_-]{1,180}$`)

func (e EnvironmentInstaller) defaults() EnvironmentInstaller {
	if e.Run == nil {
		e.Run = packageRun
	}
	if e.ABI == nil {
		e.ABI = sandbox.ABI
	}
	return e
}

func (e EnvironmentInstaller) candidate(ctx context.Context, name string) (protocol.IsolationPackage, bool) {
	output, err := e.Run(ctx, "apt-cache", "policy", name)
	if err != nil {
		return protocol.IsolationPackage{}, false
	}
	installed, candidate := "", ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Installed:") {
			installed = strings.TrimSpace(strings.TrimPrefix(line, "Installed:"))
		}
		if strings.HasPrefix(line, "Candidate:") {
			candidate = strings.TrimSpace(strings.TrimPrefix(line, "Candidate:"))
		}
	}
	if !packageVersion.MatchString(candidate) || candidate == installed {
		return protocol.IsolationPackage{}, false
	}
	return protocol.IsolationPackage{Name: name, Version: candidate}, true
}

func (e EnvironmentInstaller) Plan(ctx context.Context, revision string) protocol.IsolationEnvironment {
	e = e.defaults()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p := protocol.IsolationEnvironment{State: "blocked", Packages: []protocol.IsolationPackage{}, Checks: []protocol.IsolationCheck{}}
	abi, abiErr := e.ABI()
	p.Checks = append(p.Checks, protocol.IsolationCheck{ID: "landlock", OK: abiErr == nil, Detail: fmt.Sprintf("Landlock ABI %d; required: 9+", abi)})
	help, err := e.Run(ctx, "bwrap", "--help")
	bwrapOK := err == nil && strings.Contains(help, "--disable-userns") && strings.Contains(help, "--bind-fd") && strings.Contains(help, "--ro-bind-fd")
	p.Checks = append(p.Checks, protocol.IsolationCheck{ID: "bubblewrap", OK: bwrapOK, Detail: "bubblewrap with namespace and descriptor mount support"})
	if abiErr == nil && bwrapOK {
		p.State = "verify"
		return fingerprint(p, revision)
	}
	data, err := os.ReadFile(filepath.Join(e.Root, "/etc/os-release"))
	distro, version := "", ""
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, found := strings.Cut(line, "=")
			if !found {
				continue
			}
			value = strings.Trim(value, "\"'")
			if key == "ID" {
				distro = value
			}
			if key == "VERSION_ID" {
				version = value
			}
		}
	}
	if distro != "debian" && distro != "ubuntu" {
		p.Error = "Automatic installation currently requires Debian or Ubuntu with configured distribution repositories."
		return fingerprint(p, revision)
	}
	if !bwrapOK {
		if pkg, ok := e.candidate(ctx, "bubblewrap"); ok {
			p.Packages = append(p.Packages, pkg)
		} else {
			p.Error = "The configured repositories have no bubblewrap update with the required features."
		}
	}
	if abiErr != nil {
		// Containers share their host kernel; installing a kernel inside them
		// cannot change the syscall ABI. Never suggest such an installation.
		_, dockerErr := os.Stat(filepath.Join(e.Root, "/.dockerenv"))
		_, podmanErr := os.Stat(filepath.Join(e.Root, "/run/.containerenv"))
		if dockerErr == nil || podmanErr == nil {
			p.Error = "The container uses its host kernel. Upgrade and restart the host before checking isolation again."
		} else {
			var names []string
			if distro == "debian" {
				if runtime.GOARCH == "amd64" {
					names = []string{"linux-image-amd64"}
				} else if runtime.GOARCH == "arm64" {
					names = []string{"linux-image-arm64"}
				}
			} else {
				if regexp.MustCompile(`^\d{2}\.04$`).MatchString(version) {
					names = append(names, "linux-generic-hwe-"+version)
				}
				names = append(names, "linux-generic")
			}
			kernelAdded := false
			running, runningErr := e.Run(ctx, "uname", "-r")
			for _, name := range names {
				if pkg, ok := e.candidate(ctx, name); ok && runningErr == nil && newerRelease(pkg.Version, strings.TrimSpace(running)) {
					p.Packages = append(p.Packages, pkg)
					p.RequiresReboot = true
					kernelAdded = true
					break
				}
			}
			if !kernelAdded {
				if e.newerKernelInstalled(ctx) {
					p.RequiresReboot = true
				} else {
					p.Error = "No newer distribution kernel is available in the configured repositories. A distribution/kernel upgrade is required for Landlock ABI 9; installing bubblewrap alone will not enable isolation."
				}
			}
		}
	}
	// Do not perform a partial install while a known host requirement remains
	// unsatisfied. A kernel candidate is an upgrade attempt, never proof of ABI.
	p.CanInstall = len(p.Packages) > 0 && p.Error == ""
	if p.CanInstall {
		p.State = "install_required"
	} else if p.RequiresReboot && p.Error == "" {
		p.State = "reboot_required"
	}
	return fingerprint(p, revision)
}

func (e EnvironmentInstaller) newerKernelInstalled(ctx context.Context) bool {
	current, err := e.Run(ctx, "uname", "-r")
	if err != nil {
		return false
	}
	current = strings.TrimSpace(current)
	entries, err := os.ReadDir(filepath.Join(e.Root, "/boot"))
	if err != nil {
		return false
	}
	// Compare numeric release components; never infer Landlock support from a
	// version. This only identifies a kernel that still needs a reboot test.
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "vmlinuz-") {
			continue
		}
		candidate := strings.TrimPrefix(entry.Name(), "vmlinuz-")
		if newerRelease(candidate, current) {
			return true
		}
	}
	return false
}

func newerRelease(candidate, current string) bool {
	parts := func(s string) ([]int, bool) {
		if _, tail, ok := strings.Cut(s, ":"); ok {
			s = tail
		}
		if !regexp.MustCompile(`^\d+\.\d+`).MatchString(s) {
			return nil, false
		}
		matches := regexp.MustCompile(`\d+`).FindAllString(s, 4)
		out := make([]int, 4)
		for i, v := range matches {
			if _, err := fmt.Sscanf(v, "%d", &out[i]); err != nil {
				return nil, false
			}
		}
		return out, true
	}
	a, ok := parts(candidate)
	if !ok {
		return false
	}
	b, ok := parts(current)
	if !ok {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func fingerprint(p protocol.IsolationEnvironment, revision string) protocol.IsolationEnvironment {
	raw, _ := json.Marshal(struct {
		Revision string
		Plan     protocol.IsolationEnvironment
	}{revision, p})
	sum := sha256.Sum256(raw)
	p.PlanID = hex.EncodeToString(sum[:])
	return p
}

func (e EnvironmentInstaller) Install(ctx context.Context, revision, planID string) (result protocol.IsolationEnvironment) {
	defer func() {
		if result.State == "failed" {
			result.CanInstall = false
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	e = e.defaults()
	p := e.Plan(ctx, revision)
	if !validRevision(planID) || p.PlanID != planID || !p.CanInstall {
		p.State = "failed"
		p.Error = "The installation plan changed or cannot be installed. Check the environment and confirm the new plan."
		return p
	}
	logs := []string{"Installation confirmed; existing isolation and sudo settings are unchanged."}
	// Update metadata only after confirmation. A changed candidate requires a
	// new confirmation, rather than silently installing a different kernel.
	if output, err := e.Run(ctx, "apt-get", "-o", "DPkg::Lock::Timeout=60", "update"); err != nil {
		p.State = "failed"
		p.Error = packageFailure(output)
		p.Logs = append(logs, "Repository refresh failed.")
		return p
	}
	fresh := e.Plan(ctx, revision)
	if fresh.PlanID != planID {
		fresh.State = "failed"
		fresh.Error = "Repository candidates changed. Confirm the refreshed installation plan."
		fresh.Logs = logs
		return fresh
	}
	args := []string{"-o", "DPkg::Lock::Timeout=60", "--assume-yes", "--no-install-recommends", "--no-remove", "install"}
	for _, pkg := range p.Packages {
		args = append(args, pkg.Name+"="+pkg.Version)
	}
	output, err := e.Run(ctx, "apt-get", args...)
	if err != nil {
		p.State = "failed"
		p.Error = packageFailure(output)
		p.Logs = append(logs, "Package installation failed; check the environment before retrying.")
		return p
	}
	for _, pkg := range p.Packages {
		out, err := e.Run(ctx, "dpkg-query", "-W", "-f=${Status}\t${Version}", pkg.Name)
		if err != nil || strings.TrimSpace(out) != "install ok installed\t"+pkg.Version {
			p.State = "failed"
			p.Error = "Package verification failed: " + pkg.Name
			p.Logs = logs
			return p
		}
		logs = append(logs, "Installed and verified: "+pkg.Name+" "+pkg.Version)
	}
	p.CanInstall = false
	p.Logs = logs
	p.Error = ""
	if p.RequiresReboot {
		p.State = "reboot_required"
		p.Logs = append(p.Logs, "A server reboot is required. Isolation remains unchanged until the running kernel passes the full check.")
	} else {
		p.State = "verify"
	}
	return p
}

func packageFailure(output string) string {
	// Package output may contain authenticated mirror URLs. Return a bounded
	// cause, never raw repository output or the process environment.
	for _, item := range []struct{ needle, message string }{
		{"lock", "The package manager is busy. Retry after other package operations finish."},
		{"No space left", "Insufficient disk space for installation."},
		{"not signed", "Repository signature verification failed."},
		{"404", "The package candidate could not be downloaded. Refresh the installation plan."},
		{"resolve", "The repository hostname could not be resolved."},
	} {
		if strings.Contains(output, item.needle) {
			return item.message
		}
	}
	return "The distribution package manager failed. Check repository access and package-manager health, then check the environment again."
}
