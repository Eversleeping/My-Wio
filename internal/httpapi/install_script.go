package httpapi

import (
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"io"
	"net/http"
	"strings"
)

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
[ "$(uname -s)" = Linux ] || { echo 'Only Linux is supported.' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo 'Run this installer as root (sudo).' >&2; exit 1; }
[ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1 || { echo 'A running systemd is required.' >&2; exit 1; }
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
case "$(uname -m)" in
`)
	for _, arch := range []string{"amd64", "arm64"} {
		pkg, ok := command.Packages[arch]
		if !ok {
			continue
		}
		pattern := "x86_64|amd64"
		if arch == "arm64" {
			pattern = "aarch64|arm64"
		}
		fmt.Fprintf(w, " %s) arch=%s; expected=%s ;;\n", pattern, registrationShellQuote(arch), registrationShellQuote(pkg.SHA256))
	}
	fmt.Fprint(w, ` *) echo 'No installer package for this architecture.' >&2; exit 1 ;;
esac
umask 077
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT HUP INT TERM
`)
	fmt.Fprintf(w, "curl -fsSL --retry 3 \"$control_url/api/agent/install-package/$arch?version=%s&sha256=$expected\" -o \"$work/wio-agent\"\n", buildinfo.Version)
	io.WriteString(w, `printf '%s  %s\n' "$expected" "$work/wio-agent" | sha256sum -c -
chmod 0700 "$work/wio-agent"
"$work/wio-agent" install --url "$control_url" --registration "$registration"
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
