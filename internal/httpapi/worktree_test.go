package httpapi

import (
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/store"
)

func TestNewWorktreeCommandDefaultsPath(t *testing.T) {
	workspace := store.Workspace{ID: "source", ProjectID: "project", Path: filepath.Join(t.TempDir(), "repo")}
	command, err := newWorktreeCommand(workspace, createWorktreeInput{Branch: "feature/images"})
	if err != nil {
		t.Fatal(err)
	}
	if command.TargetWorkspaceID == "" || command.SourceWorkspaceID != workspace.ID || command.ProjectID != workspace.ProjectID {
		t.Fatalf("unexpected identifiers: %#v", command)
	}
	expected := filepath.Join(filepath.Dir(workspace.Path), "repo-feature-images")
	if command.TargetPath != expected || command.Branch != "feature/images" {
		t.Fatalf("unexpected defaults: %#v", command)
	}
}

func TestNewWorktreeCommandRejectsRelativePath(t *testing.T) {
	_, err := newWorktreeCommand(store.Workspace{ID: "source", ProjectID: "project", Path: filepath.Join(t.TempDir(), "repo")}, createWorktreeInput{Branch: "feature/test", Path: "relative"})
	if err == nil {
		t.Fatal("expected relative path rejection")
	}
}
