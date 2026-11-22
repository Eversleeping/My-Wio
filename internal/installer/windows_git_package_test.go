package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWindowsGitPackageBuildContract(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../deploy/controlplane.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{WindowsGitArchive, WindowsGitSHA256, WindowsGitDownloadURL} {
		if !strings.Contains(string(raw), value) {
			t.Fatal("control-plane build does not match the published Git package metadata")
		}
	}
	if !strings.Contains(windowsGitScript, "Get-FileHash") || !strings.Contains(windowsGitScript, "Git version verification failed") || strings.Contains(windowsGitScript, "api.github.com") {
		t.Fatal("Git bootstrap must verify Wio's package and the extracted executable")
	}
}
