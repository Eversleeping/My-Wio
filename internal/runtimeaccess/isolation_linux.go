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
	distro := ""
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
		}
	}
	if distro != "debian" && distro != "ubuntu" {
		p.Error = "Automatic installation currently requires Debian or Ubuntu with configured distribution repositories."
		return fingerprint(p, revision)
	}

	if abiErr != nil {
		p.Packages = []protocol.IsolationPackage{}
		p.Error = "The running kernel does not support the required isolation boundary. This installer only supports components that need no server reboot; kernel and system upgrades are excluded. Ordinary sessions remain available with isolation disabled."
		return fingerprint(p, revision)
	}
	if !bwrapOK {
		if pkg, ok := e.candidate(ctx, "bubblewrap"); ok {
			p.Packages = append(p.Packages, pkg)
		} else {
			p.Error = "The configured repositories have no bubblewrap update with the required features."
		}
	}
	if len(p.Packages) > 0 && p.Error == "" {
		p.Error = e.checkUserspaceTransaction(ctx, p.Packages)
	}
	p.CanInstall = len(p.Packages) > 0 && p.Error == ""
	if p.CanInstall {
		p.State = "install_required"
	}
	return fingerprint(p, revision)
}

// Refuse system changes hidden in dependency resolution. A bubblewrap-only
// transaction cannot upgrade a kernel, bootloader, libc or system service.
func (e EnvironmentInstaller) checkUserspaceTransaction(ctx context.Context, packages []protocol.IsolationPackage) string {
	if reason := e.pendingReboot(); reason != "" {
		return reason
	}
	if len(packages) != 1 || packages[0].Name != "bubblewrap" {
		return "Only a bubblewrap-only installation is supported; kernel and system changes are excluded."
	}
	output, err := e.Run(ctx, "apt-get", "--simulate", "--no-install-recommends", "--no-remove", "install", "bubblewrap="+packages[0].Version)
	if err != nil {
		return packageFailure(output)
	}
	if len(output) >= 4096 {
		return "The dependency plan is too large to verify a userspace-only installation."
	}
	installs := 0
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "Remv", "Purg":
			return "The package plan would remove system packages. No installation will be performed."
		case "Inst", "Conf":
			if len(fields) < 2 || strings.Split(fields[1], ":")[0] != "bubblewrap" {
				return "The package plan requires changes beyond bubblewrap. Kernel and system package changes are excluded."
			}
			if fields[0] == "Inst" {
				installs++
			}
		}
	}
	if installs != 1 {
		return "A bubblewrap-only package transaction could not be verified. Check the environment again."
	}
	return ""
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
	// new confirmation, rather than silently installing another package version.
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
	if reason := e.pendingReboot(); reason != "" {
		p.State, p.Error = "blocked", reason
		return p
	}
	p.State = "verify"
	return p
}

func (e EnvironmentInstaller) pendingReboot() string {
	for _, name := range []string{"/run/reboot-required", "/var/run/reboot-required"} {
		if _, err := os.Stat(filepath.Join(e.Root, name)); err == nil {
			return "The system reports a pending server reboot. Isolation has not been enabled; no server restart action is provided."
		}
	}
	return ""
}

func packageFailure(output string) string {
	// Package output may contain authenticated mirror URLs. Return a bounded
	// cause, never raw repository output or the process environment.
	for _, item := range []struct{ needle, message string }{
		{"lock", "The package manager is busy. Retry after other package operations finish."},
		{"No space left", "Insufficient disk space for installation."},
		{"not signed", "Repository signature verification failed."},
		{"404", "The package candidate could not be downloaded. Refresh the installation plan."},
		{"Could not resolve", "The repository hostname could not be resolved."},
		{"Temporary failure resolving", "The repository hostname could not be resolved."},
		{"unmet dependencies", "Package dependencies cannot be resolved without additional system changes."},
	} {
		if strings.Contains(output, item.needle) {
			return item.message
		}
	}
	return "The distribution package manager failed. Check repository access and package-manager health, then check the environment again."
}
