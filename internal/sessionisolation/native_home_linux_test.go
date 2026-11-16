//go:build linux

package sessionisolation

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestGeminiHomeExistsBeforeTrustedSettingsAreWritten(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "gemini-home")
	env := []string{"HOME=" + root, "GEMINI_CLI_HOME=" + home}
	if err := PrepareGeminiHome(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("disabled isolation created a native home")
	}
	manager := New(&Config{Enabled: true}, "")
	if os.Geteuid() == 0 {
		manager.options.UID, manager.options.GID = 1000, 1000
	}
	ctx := manager.Context(context.Background())
	if err := PrepareGeminiHome(ctx, env); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{home, filepath.Join(home, ".gemini")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		wantUID, wantGID := uint32(os.Geteuid()), uint32(os.Getegid())
		if os.Geteuid() == 0 {
			wantUID, wantGID = 1000, 1000
		}
		if owner.Uid != wantUID || owner.Gid != wantGID || info.Mode().Perm() != 0700 {
			t.Fatal("new Gemini native directories are inaccessible to the isolated account")
		}
	}
	// Existing directories belong to the administrator, including deliberately
	// inaccessible ones. Preparing a later turn must not change their ownership.
	existing := filepath.Join(root, ".gemini")
	if err := os.Mkdir(existing, 0750); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(existing)
	if err := PrepareGeminiHome(ctx, []string{"HOME=" + root}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(existing)
	oldOwner, newOwner := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if oldOwner.Uid != newOwner.Uid || oldOwner.Gid != newOwner.Gid || before.Mode() != after.Mode() {
		t.Fatal("preparing Gemini settings changed administrator-owned permissions")
	}
}
