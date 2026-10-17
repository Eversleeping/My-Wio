package scanner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestWriteWorkspaceFileCreateEditAndConflict(t *testing.T) {
	root := t.TempDir()
	command := protocol.WorkspaceFileWriteCommand{Root: root, Path: "docs/说明.txt", Content: []byte("原文\r\n"), Create: true}
	write := func() error { return WriteWorkspaceFile(context.Background(), command, []string{root}) }
	if err := write(); err != nil {
		t.Fatal(err)
	}
	if err := write(); err == nil {
		t.Fatal("create overwrote existing file")
	}
	file := filepath.Join(root, "docs", "说明.txt")
	if err := os.Chmod(file, 0755); err != nil {
		t.Fatal(err)
	}
	command.Create = false
	command.ExpectedHash = fmt.Sprintf("%x", sha256.Sum256(command.Content))
	command.Content = []byte("修改\r\n")
	if err := write(); err != nil {
		t.Fatal(err)
	}
	if err := write(); err == nil {
		t.Fatal("stale edit was accepted")
	}
	content, _ := os.ReadFile(file)
	if string(content) != "修改\r\n" {
		t.Fatalf("unexpected file contents: %q", content)
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm()&0200 == 0 {
		t.Fatal("file permissions lost")
	}
	command.Path = "binary.dat"
	command.Create = true
	command.Content = []byte{0, 0xff, 1}
	if err := write(); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(filepath.Join(root, "binary.dat"))
	if string(content) != string(command.Content) {
		t.Fatal("upload bytes changed")
	}
	entries, _ := os.ReadDir(filepath.Join(root, "docs"))
	if len(entries) != 1 {
		t.Fatal("temporary write files leaked")
	}
}

func TestWriteWorkspaceFileRejectsUnsafePathsAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"", "../outside", "/tmp/outside", "nested/../file", ".git/config", "nested/.git/config", "C:/outside", "nested//file"} {
		if err := WriteWorkspaceFile(context.Background(), protocol.WorkspaceFileWriteCommand{Root: root, Path: path, Create: true}, []string{root}); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
	if err := WriteWorkspaceFile(context.Background(), protocol.WorkspaceFileWriteCommand{Root: root, Path: "large", Create: true, Content: make([]byte, protocol.MaxWorkspaceFileBytes+1)}, []string{root}); err == nil {
		t.Fatal("oversized file accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteWorkspaceFile(ctx, protocol.WorkspaceFileWriteCommand{Root: root, Path: "cancelled", Create: true}, []string{root}); err == nil {
		t.Fatal("cancelled write accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled")); !os.IsNotExist(err) {
		t.Fatal("cancelled write created a file")
	}
	if err := WriteWorkspaceFile(context.Background(), protocol.WorkspaceFileWriteCommand{Root: root, Path: "denied", Create: true}, []string{t.TempDir()}); err == nil {
		t.Fatal("write outside allowed roots accepted")
	}
}

func TestWriteWorkspaceFileRejectsSymlinkParentsAndTargets(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	command := protocol.WorkspaceFileWriteCommand{Root: root, Path: "link/escape", Content: []byte("unsafe"), Create: true}
	if err := WriteWorkspaceFile(context.Background(), command, []string{root}); err == nil {
		t.Fatal("followed symlink parent")
	}
	if err := os.WriteFile(filepath.Join(outside, "file"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "file"), filepath.Join(root, "file")); err != nil {
		t.Fatal(err)
	}
	command.Path = "file"
	command.Create = false
	command.ExpectedHash = fmt.Sprintf("%x", sha256.Sum256([]byte("original")))
	if err := WriteWorkspaceFile(context.Background(), command, []string{root}); err == nil {
		t.Fatal("edited symlink target")
	}
	content, _ := os.ReadFile(filepath.Join(outside, "file"))
	if string(content) != "original" {
		t.Fatal("external file changed")
	}
}
