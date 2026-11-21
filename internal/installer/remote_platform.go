package installer

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/hostplatform"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

func powershellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func powershellCommand(script string) string {
	words := utf16.Encode([]rune("$ErrorActionPreference='Stop'; " + script))
	bytes := make([]byte, len(words)*2)
	for i, w := range words {
		binary.LittleEndian.PutUint16(bytes[i*2:], w)
	}
	return "powershell.exe -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(bytes)
}
func inspectWindows(client commandHost, hostname string) (inspection, error) {
	raw, err := run(client, powershellCommand(`$p=New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent()); @{architecture=[System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant();admin=$p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)} | ConvertTo-Json -Compress`))
	if err != nil {
		return inspection{}, ErrConnection
	}
	var probe struct {
		Architecture string
		Admin        bool
	}
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &probe) != nil {
		return inspection{}, ErrUnsupportedPlatform
	}
	return inspection{OS: "windows", Architecture: normalizedArchitecture(probe.Architecture), Hostname: strings.TrimSpace(hostname), SudoReady: probe.Admin}, nil
}
func (s *Service) stageWindowsInstaller(client *sshHost, probe inspection) (string, func(), error) {
	if !probe.SudoReady {
		return "", nil, ErrPrivilegeRequired
	}
	asset, err := platformAgentAsset("windows", probe.Architecture)
	if err != nil {
		return "", nil, err
	}
	binary, err := os.ReadFile(filepath.Join(s.assetDir, asset))
	if err != nil {
		return "", nil, ErrAssetsUnavailable
	}
	dir, err := runTrimmed(client, powershellCommand(`$root=Join-Path ([Environment]::GetFolderPath('CommonApplicationData')) 'WioInstaller';New-Item -Force -ItemType Directory $root | Out-Null; & icacls.exe $root /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' /Q | Out-Null;if($LASTEXITCODE -ne 0){throw 'ACL failed'};$path=Join-Path $root ('stage-'+[Guid]::NewGuid().ToString('N'));New-Item -ItemType Directory $path | Out-Null;[Console]::Write($path)`))
	if err != nil || !validWindowsStagingDirectory(dir) {
		return "", nil, ErrInstallation
	}
	cleanup := func() { _, _ = run(client, powershellCommand("Remove-Item -Recurse -Force "+powershellQuote(dir))) }
	path := dir + `\wio-agent.exe`
	_, err = runWithInput(client, powershellCommand("[IO.File]::WriteAllBytes("+powershellQuote(path)+",[Convert]::FromBase64String([Console]::In.ReadToEnd()))"), []byte(base64.StdEncoding.EncodeToString(binary)))
	if err != nil {
		cleanup()
		return "", nil, ErrInstallation
	}
	return path, cleanup, nil
}
func (s *Service) launchWindowsInstaller(ctx context.Context, client *sshHost, probe inspection, request InstallRequest) (InstallResult, error) {
	path, cleanup, err := s.stageWindowsInstaller(client, probe)
	if err != nil {
		return InstallResult{}, err
	}
	defer cleanup()
	return launchRemoteSession(client, powershellCommand("$OutputEncoding=New-Object System.Text.UTF8Encoding($false);[Console]::OutputEncoding=$OutputEncoding;$plan=[Console]::In.ReadToEnd();$plan | & "+powershellQuote(path)+" install --request-stdin;exit $LASTEXITCODE"), request)
}
func (s *Service) uninstallRemotePortable(ctx context.Context, client *sshHost, probe inspection, request UninstallRequest) (UninstallResult, error) {
	if !probe.SudoReady {
		return UninstallResult{}, ErrPrivilegeRequired
	}
	var command string
	if probe.OS == "windows" {
		path, cleanup, err := s.stageWindowsInstaller(client, probe)
		if err != nil {
			return UninstallResult{}, err
		}
		defer cleanup()
		command = powershellCommand("& " + powershellQuote(path) + " uninstall --server " + powershellQuote(request.ExpectedServerID) + ";exit $LASTEXITCODE")
	} else if probe.OS == "darwin" {
		// A copy is needed because uninstall removes the installed binary.
		command = elevated(isRoot(client), `set -eu; work=$(mktemp -d /tmp/wio-install.XXXXXXXXXX); trap 'rm -rf "$work"' EXIT; cp /usr/local/bin/wio-agent "$work/wio-agent"; "$work/wio-agent" uninstall --server `+shellQuote(request.ExpectedServerID))
	} else {
		return UninstallResult{}, ErrUnsupportedPlatform
	}
	raw, err := run(client, command)
	if err != nil {
		return UninstallResult{}, ErrUninstallation
	}
	var result UninstallResult
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &result) != nil || result.ServerID != request.ExpectedServerID {
		return UninstallResult{}, ErrUninstallation
	}
	return result, nil
}
func UninstallLocal(ctx context.Context, expected string) (UninstallResult, error) {
	if !hostplatform.Administrator() || !validServerID(expected) {
		return UninstallResult{}, ErrInvalidTarget
	}
	l := hostplatform.Current()
	raw, err := os.ReadFile(l.Config)
	if err != nil {
		return UninstallResult{}, err
	}
	var config struct {
		ServerID string `json:"server_id"`
	}
	if json.Unmarshal(raw, &config) != nil || config.ServerID != expected {
		return UninstallResult{}, ErrServerMismatch
	}
	if err := uninstallPortableLocal(ctx, l); err != nil {
		return UninstallResult{}, errors.New("could not uninstall Agent")
	}
	hostname, _ := os.Hostname()
	return UninstallResult{ServerID: expected, Hostname: hostname}, nil
}

// PowerShellQuote quotes a literal without expanding variables or subexpressions.
func PowerShellQuote(value string) string { return powershellQuote(value) }

func validWindowsStagingDirectory(dir string) bool {
	marker := `\WioInstaller\stage-`
	index := strings.LastIndex(dir, marker)
	if index < 3 || len(dir[index+len(marker):]) != 32 || !hostplatform.Absolute("windows", dir) {
		return false
	}
	suffix := dir[index+len(marker):]
	for _, r := range suffix {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
