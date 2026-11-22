package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsCredentialAtomicReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.key")
	for _, value := range []string{"first-synthetic-key", "replacement-synthetic-key"} {
		if err := replaceCredentialFile(path, []byte(value)); err != nil {
			t.Fatal(err)
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != value {
			t.Fatal("credential replacement was not published")
		}
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if err := replaceCredentialFile(path, []byte("must-not-replace")); err == nil {
		t.Fatal("real access errors must not be ignored")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "replacement-synthetic-key" {
		t.Fatal("failed replacement damaged the original credential")
	}
}
