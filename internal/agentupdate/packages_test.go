package agentupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRetriesUnavailableAndTransientErrors(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if _, err := s.Command(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable: %v", err)
	}
	path := filepath.Join(dir, "wio-agent-linux-amd64")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Command(); err == nil {
		t.Fatal("directory accepted as package")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Package("amd64"); err != nil {
		t.Fatalf("failure was cached permanently: %v", err)
	}
}

func TestStoreReloadsReplacedChangedAddedAndRemovedPackages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wio-agent-linux-amd64")
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	write(path, "first")
	s := New(dir)
	first, err := s.Package("amd64")
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement")
	write(replacement, "other") // Same size and mtime; replacement inode must invalidate the cache.
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	second, err := s.Package("amd64")
	if err != nil || second.SHA256 == first.SHA256 {
		t.Fatalf("replacement was not rehashed: %+v %v", second, err)
	}
	write(path, "different-length")
	third, err := s.Package("amd64")
	hash := sha256.Sum256([]byte("different-length"))
	if err != nil || third.SHA256 != hex.EncodeToString(hash[:]) || third.Size != int64(len("different-length")) {
		t.Fatalf("changed file was not reloaded: %+v %v", third, err)
	}
	write(filepath.Join(dir, "wio-agent-linux-arm64"), "arm-package")
	command, err := s.Command()
	if err != nil || len(command.Packages) != 2 {
		t.Fatalf("added package missing: %+v %v", command, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Package("amd64"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("removed package remained cached: %v", err)
	}
	if _, err := s.Package("arm64"); err != nil {
		t.Fatal(err)
	}
}
