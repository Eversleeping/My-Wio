//go:build windows

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/agentupdate"
)

// Execute the served installer, including administrator/architecture preflight,
// ACL setup, package checksum, and native executable launch. The probe replaces
// only the final Agent executable; it does not create a service or register a host.
func TestWindowsRegistrationInstallerExecution(t *testing.T) {
	assets := t.TempDir()
	probe := filepath.Join(assets, "probe.go")
	if err := os.WriteFile(probe, []byte(`package main
import("bufio";"fmt";"os";"strings")
func main(){
 if strings.Join(os.Args[1:],"|")!="install|--url|"+os.Getenv("WIO_TEST_ORIGIN")+"|--registration|test-registration"{panic("incorrect installer arguments")}
 code,_:=bufio.NewReader(os.Stdin).ReadString('\n')
 if strings.TrimSpace(code)!="test-code"{panic("registration prompt lost stdin")}
 fmt.Println("WIO_PROBE_INSTALLED")
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(assets, "wio-agent-windows-amd64.exe")
	build := exec.Command("go", "build", "-o", exe, probe)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build installer probe: %v\n%s", err, output)
	}
	api := &API{agentUpdates: agentupdate.New(assets)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/install.ps1":
			api.agentInstallPowerShell(w, r)
		case "/api/agent/install-package/windows-amd64":
			http.ServeFile(w, r, exe)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("WIO_TEST_ORIGIN", server.URL)
	tmp := filepath.Join(t.TempDir(), "installation files")
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windowsRegistrationCommand(server.URL, "test-registration"))
	cmd.Stdin = strings.NewReader("test-code\n")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "WIO_PROBE_INSTALLED") {
		t.Fatalf("served Windows installer failed: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("installer temporary files were not cleaned up: %v, %v", entries, err)
	}
}
