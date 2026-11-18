package gitrepository

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/taskartifact"
)

func TestTaskImmutableHandoff(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "second-server")
	ctx := context.Background()
	runChangesGit(t, source, "init", "-b", "main")
	runChangesGit(t, source, "config", "user.name", "Artifact Test")
	runChangesGit(t, source, "config", "user.email", "artifact@example.test")
	writeChangesFile(t, source, "modified.txt", "before\n")
	writeChangesFile(t, source, "deleted.txt", "remove\n")
	writeChangesFile(t, source, ".gitignore", "ignored.txt\n")
	runChangesGit(t, source, "add", ".")
	runChangesGit(t, source, "commit", "-m", "base")
	runChangesGit(t, source, "clone", "--quiet", "--no-local", source, target)

	writeChangesFile(t, source, "modified.txt", "captured\n")
	writeChangesFile(t, source, "ignored.txt", "ignored fixture")
	if err := os.Remove(filepath.Join(source, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0, 1, 3, 255}
	if err := os.WriteFile(filepath.Join(source, "new.bin"), payload, 0755); err != nil {
		t.Fatal(err)
	}
	manifest, err := CaptureTaskArtifact(ctx, source, []string{"modified.txt", "deleted.txt", "new.bin"})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := taskartifact.Validate(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeChangesFile(t, source, "modified.txt", "developed since capture\n")
	command := protocol.TaskArtifactHandoff{Action: "preflight", Digest: digest, Manifest: manifest}
	if err := HandoffTaskArtifact(ctx, target, command); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ignored.txt", "../outside", ".git/config", "missing.txt"} {
		if _, err := CaptureTaskArtifact(ctx, source, []string{name}); err == nil {
			t.Fatalf("captured excluded file %q", name)
		}
	}
	command.Action = "apply"
	command.Digest = "incorrect"
	if err := HandoffTaskArtifact(ctx, target, command); err == nil {
		t.Fatal("accepted invalid digest")
	}
	command.Digest = digest
	if err := os.Remove(filepath.Join(target, "modified.txt")); err != nil {
		t.Fatal(err)
	}
	if err := HandoffTaskArtifact(ctx, target, command); err == nil {
		t.Fatal("accepted missing target baseline file")
	}
	writeChangesFile(t, target, "modified.txt", "before\n")
	if _, err := os.Stat(filepath.Join(target, "new.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation changed target files")
	}
	for i := 0; i < 2; i++ {
		if err := HandoffTaskArtifact(ctx, target, command); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := os.ReadFile(filepath.Join(target, "modified.txt"))
	if err != nil || string(actual) != "captured\n" {
		t.Fatalf("capture changed with source: %q %v", actual, err)
	}
	bin, err := os.ReadFile(filepath.Join(target, "new.bin"))
	if err != nil || !bytes.Equal(bin, payload) {
		t.Fatalf("binary content changed: %v %v", bin, err)
	}
	if _, err := os.Stat(filepath.Join(target, "deleted.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deletion was not reproduced")
	}
	if data, err := InspectOutput(ctx, target, "diff", "--cached", "--name-only"); err != nil || len(data) != 0 {
		t.Fatal("handoff staged changes", err)
	}
	if head, err := artifactBaseline(ctx, target); err != nil || head != manifest.BaseSHA {
		t.Fatal("handoff changed HEAD", err)
	}
}
