package gitrepository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestTaskHandoffTransitionsBetweenLegalSnapshots(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runChangesGit(t, root, "init", "-b", "main")
	runChangesGit(t, root, "-c", "user.name=Artifact Test", "-c", "user.email=artifact@example.test", "commit", "--allow-empty", "-m", "base")
	base, err := artifactBaseline(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	file := func(path string) protocol.ArtifactFile {
		data := []byte(path + "\n")
		return protocol.ArtifactFile{Path: path, AfterMode: "100644", AfterHash: taskartifact.Hash(data), Content: data}
	}
	first := protocol.ArtifactManifest{BaseSHA: base}
	for i := 0; i < protocol.MaxArtifactFiles-1; i++ {
		first.Files = append(first.Files, file(fmt.Sprintf("old-%03d", i)))
	}
	first.Files = append(first.Files, file("item/old.txt"))
	second := protocol.ArtifactManifest{BaseSHA: base, Files: []protocol.ArtifactFile{file("item"), file("next.txt")}}
	var previous *protocol.ArtifactManifest
	for _, manifest := range []protocol.ArtifactManifest{first, second, first} {
		digest, err := taskartifact.Validate(manifest)
		if err != nil {
			t.Fatal(err)
		}
		command := protocol.TaskArtifactHandoff{Action: "apply", Manifest: manifest, Digest: digest, Previous: previous}
		if err := HandoffTaskArtifact(ctx, root, command); err != nil {
			t.Fatal(err)
		}
		command.Action, command.Previous, command.MetadataOnly = "verify", nil, true
		command.Manifest.Files = append([]protocol.ArtifactFile(nil), manifest.Files...)
		for i := range command.Manifest.Files {
			command.Manifest.Files[i].Content = nil
		}
		if err := HandoffTaskArtifact(ctx, root, command); err != nil {
			t.Fatal(err)
		}
		previous = &manifest
	}
}

func TestTaskHandoffRestoresOnlyRecognizedContents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runChangesGit(t, root, "init", "-b", "main")
	runChangesGit(t, root, "-c", "user.name=Artifact Test", "-c", "user.email=artifact@example.test", "commit", "--allow-empty", "-m", "base")
	base, err := artifactBaseline(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	before := artifactContent{mode: "100644", data: []byte("before\n")}
	after := []byte("after\n")
	files := []protocol.ArtifactFile{{Path: "edited.txt", AfterMode: "100644", AfterHash: taskartifact.Hash(after), Content: after}}
	writeChangesFile(t, root, "edited.txt", string(after))
	if err := restoreArtifactTransition(ctx, root, base, files, []artifactContent{before}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "edited.txt"))
	if err != nil || !bytes.Equal(got, before.data) {
		t.Fatalf("restore did not recover the prior contents: %q %v", got, err)
	}
	writeChangesFile(t, root, "edited.txt", "other change\n")
	if err := restoreArtifactTransition(ctx, root, base, files, []artifactContent{before}); !errors.Is(err, ErrArtifactHandoffUnknown) {
		t.Fatalf("modified worktree was reported as restored: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(root, "edited.txt"))
	if err != nil || string(got) != "other change\n" {
		t.Fatalf("restore overwrote an unrelated change: %q %v", got, err)
	}
}
