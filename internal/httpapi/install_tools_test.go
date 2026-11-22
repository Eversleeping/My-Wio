package httpapi

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/agentupdate"
	"github.com/wio-platform/wio/internal/installer"
)

func TestWindowsGitPackageDownload(t *testing.T) {
	assets := t.TempDir()
	for _, name := range []string{"wio-agent-windows-amd64.exe", installer.WindowsGitArchive} {
		if err := os.WriteFile(filepath.Join(assets, name), []byte("package fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	api := &API{agentUpdates: agentupdate.New(assets)}
	for _, tc := range []struct {
		sha    string
		status int
	}{{"wrong-package", 409}, {installer.WindowsGitSHA256, 200}} {
		response := httptest.NewRecorder()
		api.downloadWindowsGit(response, httptest.NewRequest("GET", "/api/agent/install-tools/windows-git?sha256="+tc.sha, nil))
		if response.Code != tc.status {
			t.Fatalf("package download status: %d", response.Code)
		}
		if tc.status == 200 && (response.Body.String() != "package fixture" || response.Header().Get("X-Checksum-SHA256") != installer.WindowsGitSHA256) {
			t.Fatal("incorrect package response")
		}
	}
}
