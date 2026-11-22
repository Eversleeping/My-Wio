package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Run the command users paste, including its HTTP download and child process.
// Native CI uses powershell.exe (Windows PowerShell 5.1), not just pwsh.
func TestWindowsRegistrationCommandExecution(t *testing.T) {
	engine, err := exec.LookPath("powershell.exe")
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Fatal("Windows PowerShell is required for registration command tests")
		}
		t.Skip("Windows PowerShell is unavailable")
	}
	for _, tc := range []struct {
		name, script, failure string
		status                int
	}{
		{"interactive installer", `param([string]$Url,[string]$Registration)
$ErrorActionPreference='Stop'
$work='installer-local-scope'
Write-Output ('WIO_URL='+$Url)
Write-Output ('WIO_REGISTRATION='+$Registration)
$code=Read-Host 'Registration code'
Write-Output ('WIO_INPUT='+$code)
`, "", http.StatusOK},
		{"download failure", "WIO_MUST_NOT_RUN", "503", http.StatusServiceUnavailable},
		{"empty download", "", "Installer download was empty", http.StatusOK},
		{"installer exception", "throw 'WIO_INSTALLER_EXCEPTION'", "WIO_INSTALLER_EXCEPTION", http.StatusOK},
		{"installer exit", "exit 23", "Installer failed (exit 23)", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.script)
			}))
			defer server.Close()
			tmp := filepath.Join(t.TempDir(), "temporary files")
			if err := os.Mkdir(tmp, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEMP", tmp)
			origin := server.URL + "/literal path/'quote&$value"
			registration := "test registration 'quote&$value"
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, engine, "-NoProfile", "-NonInteractive", "-Command", windowsRegistrationCommand(origin, registration))
			// The child installer must retain stdin for the one-time code prompt.
			cmd.Stdin = strings.NewReader("test-code\n")
			output, err := cmd.CombinedOutput()
			if tc.failure == "" {
				if err != nil {
					t.Fatalf("command failed: %v\n%s", err, output)
				}
				for _, want := range []string{"WIO_URL=" + origin, "WIO_REGISTRATION=" + registration, "WIO_INPUT=test-code"} {
					if !strings.Contains(string(output), want) {
						t.Fatalf("missing %q: %s", want, output)
					}
				}
			} else if err == nil || (tc.status == http.StatusOK && !strings.Contains(string(output), tc.failure)) {
				t.Fatalf("expected failure %q, got %v: %s", tc.failure, err, output)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("bootstrap script was not cleaned up: %v, %v", entries, err)
			}
		})
	}
}
