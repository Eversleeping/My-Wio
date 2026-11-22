//go:build (darwin || windows) && platformintegration

package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func nativeGitArchive(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return ""
	}
	file, err := os.Create(filepath.Join(t.TempDir(), WindowsGitArchive))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	client := &http.Client{Timeout: 90 * time.Second}
	response, err := client.Get(WindowsGitDownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Git archive download status: %d", response.StatusCode)
	}
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, digest), response.Body); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(digest.Sum(nil)) != WindowsGitSHA256 {
		t.Fatal("Git archive checksum mismatch")
	}
	return file.Name()
}
