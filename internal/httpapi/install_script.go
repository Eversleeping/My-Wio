package httpapi

import (
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/installer"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

func (a *API) downloadWindowsGit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("sha256") != installer.WindowsGitSHA256 {
		writeError(w, http.StatusConflict, "Git installation package changed; update Wio Agent")
		return
	}
	if a.agentUpdates == nil {
		writeError(w, http.StatusServiceUnavailable, "Git installation package unavailable")
		return
	}
	pkg, err := a.agentUpdates.Package("windows-amd64")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Git installation package unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Checksum-SHA256", installer.WindowsGitSHA256)
	http.ServeFile(w, r, filepath.Join(filepath.Dir(pkg.Path), installer.WindowsGitArchive))
}

func (a *API) agentInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.agentUpdates == nil {
		writeError(w, 503, "installation assets unavailable")
		return
	}
	command, err := a.agentUpdates.Command()
	if err != nil {
		writeError(w, 503, "installation assets unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	fmt.Fprint(w, `#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run this installer as root (sudo).' >&2; exit 1; }
case "$(uname -s)" in
 Linux) os=linux; [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1 || { echo 'A running systemd is required.' >&2; exit 1; };;
 Darwin) os=darwin;;
 *) echo 'Use the Windows PowerShell command for Windows.' >&2; exit 1;;
esac
control_url=''
registration=''
while [ "$#" -gt 0 ]; do
 case "$1" in
  --url) control_url=$2; shift 2 ;;
  --registration) registration=$2; shift 2 ;;
  *) echo 'Unknown installer option.' >&2; exit 1 ;;
 esac
done
[ -n "$control_url" ] && [ -n "$registration" ] || { echo 'Missing registration options.' >&2; exit 1; }
case "$os/$(uname -m)" in
`)
	for _, osName := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			key := osName + "-" + arch
			pkg, ok := command.Packages[key]
			if !ok {
				continue
			}
			pattern := "x86_64|" + osName + "/amd64"
			if arch == "arm64" {
				pattern = "aarch64|" + osName + "/arm64"
			}
			// Keep the original Linux architecture download keys as published aliases.
			downloadKey := key
			if osName == "linux" {
				downloadKey = arch
			}
			fmt.Fprintf(w, " %s/%s) arch=%s; expected=%s ;;\n", osName, pattern, registrationShellQuote(downloadKey), registrationShellQuote(pkg.SHA256))
		}
	}
	fmt.Fprint(w, ` *) echo 'No installer package for this operating system/architecture.' >&2; exit 1 ;;
esac
umask 077
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT HUP INT TERM
`)
	fmt.Fprintf(w, "curl -fsSL --retry 3 \"$control_url/api/agent/install-package/$arch?version=%s&sha256=$expected\" -o \"$work/wio-agent\"\n", buildinfo.Version)
	io.WriteString(w, `if [ "$os" = darwin ]; then
 printf '%s  %s\n' "$expected" "$work/wio-agent" | shasum -a 256 -c -
else
 printf '%s  %s\n' "$expected" "$work/wio-agent" | sha256sum -c -
fi
chmod 0700 "$work/wio-agent"
"$work/wio-agent" install --url "$control_url" --registration "$registration"
`)
}

func (a *API) agentInstallPowerShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.agentUpdates == nil {
		writeError(w, 503, "installation assets unavailable")
		return
	}
	pkg, err := a.agentUpdates.Package("windows-amd64")
	if err != nil {
		writeError(w, 503, "Windows installation package unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, `param([Parameter(Mandatory=$true)][string]$Url,[Parameter(Mandatory=$true)][string]$Registration)
$ErrorActionPreference='Stop'
[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12
$principal=New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if(!$principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)){throw 'Run PowerShell as Administrator.'}
if([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() -ne 'X64'){throw 'Only Windows amd64 is supported.'}
$work=Join-Path $env:TEMP ('wio-install-'+[Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $work | Out-Null
try {
 & icacls.exe $work /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' /Q | Out-Null
 if($LASTEXITCODE -ne 0){throw 'Could not secure installer directory.'}
 $exe=Join-Path $work 'wio-agent.exe'
`)
	fmt.Fprintf(w, " $expected='%s'\n Invoke-WebRequest -UseBasicParsing ($Url+'/api/agent/install-package/windows-amd64?version=%s&sha256='+$expected) -OutFile $exe\n", pkg.SHA256, buildinfo.Version)
	fmt.Fprint(w, ` if((Get-FileHash $exe -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected){throw 'Installer checksum mismatch.'}
 & $exe install --url $Url --registration $Registration
 if($LASTEXITCODE -ne 0){throw 'Agent installation failed.'}
} finally { Remove-Item -Recurse -Force $work }
`)
}

func (a *API) downloadInstallerPackage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.agentUpdates == nil {
		writeError(w, 503, "installation assets unavailable")
		return
	}
	pkg, err := a.agentUpdates.Package(chi.URLParam(r, "architecture"))
	if err != nil {
		writeError(w, 404, "installer package unavailable")
		return
	}
	if r.URL.Query().Get("version") != buildinfo.Version || r.URL.Query().Get("sha256") != pkg.SHA256 {
		writeError(w, 409, "installer package changed; run the command again")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, pkg.Path)
}

func (a *API) agentInstallStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || len(token) > 256 {
		writeError(w, 401, "Agent credentials rejected")
		return
	}
	id, err := a.store.AuthenticateAgent(r.Context(), token)
	if err != nil {
		writeError(w, 401, "Agent credentials rejected")
		return
	}
	server, err := a.store.Server(r.Context(), id)
	if err != nil {
		writeError(w, 404, "server not found")
		return
	}
	writeJSON(w, 200, map[string]any{"server_id": id, "online": server.Status == "online" && server.LastSeenAt != nil})
}
