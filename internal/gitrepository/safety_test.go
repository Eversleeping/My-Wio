//go:build unix

package gitrepository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitArgumentsNeverPassSafeDirectory(t *testing.T) {
	requireGit(t)
	repository := filepath.Join(t.TempDir(), "repo")
	runWriteGit(t, filepath.Dir(repository), "init", "-q", repository)
	ctx := context.Background()

	inspect, err := gitArguments(ctx, repository, gitInspect, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	assertHardened(t, inspect)
	assertArgumentTail(t, inspect, "-C", repository, "rev-parse", "HEAD")

	modify, err := gitArguments(ctx, repository, gitModify, "commit", "-m", "x")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(modify, []string{"-C", repository, "commit", "-m", "x"}) {
		t.Fatalf("mutating command in an owned repository changed: %#v", modify)
	}

	withCurrentUID(t, os.Geteuid()+1)
	foreign, err := gitArguments(ctx, repository, gitModify, "commit", "-m", "x")
	if err != nil {
		t.Fatal(err)
	}
	assertHardened(t, foreign)
	assertArgumentTail(t, foreign, "-C", repository, "commit", "-m", "x")

	if args, _ := gitArguments(ctx, "", gitModify, "ls-remote", "origin"); !equalStrings(args, []string{"ls-remote", "origin"}) {
		t.Fatalf("repository-independent Git arguments changed: %#v", args)
	}
}

func TestRepositoryOwnershipDecision(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repository := filepath.Join(base, "repo")
	runWriteGit(t, base, "init", "-q", "-b", "main", repository)
	runWriteGit(t, repository, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "base")
	linked := filepath.Join(base, "linked")
	runWriteGit(t, repository, "worktree", "add", "-q", "-b", "linked", linked)
	uid := os.Geteuid()

	for _, path := range []string{repository, linked} {
		if !repositoryOwnedBy(path, uid) {
			t.Fatalf("%s should be owned by uid %d", path, uid)
		}
		if repositoryOwnedBy(path, uid+1) {
			t.Fatalf("%s should not be owned by uid %d", path, uid+1)
		}
	}
	if repositoryOwnedBy(repository, -1) || repositoryOwnedBy("", uid) {
		t.Fatal("invalid ownership query was accepted")
	}

	plain := filepath.Join(base, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if repositoryOwnedBy(plain, uid) {
		t.Fatal("directory without Git metadata was treated as an owned repository")
	}
	dangling := filepath.Join(base, "dangling")
	if err := os.Mkdir(dangling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dangling, ".git"), []byte("gitdir: "+filepath.Join(base, "missing")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if repositoryOwnedBy(dangling, uid) {
		t.Fatal("dangling .git file was treated as an owned repository")
	}
}

func TestParseFilterOverridesSkipsProtectedScopes(t *testing.T) {
	scoped := "system\x00filter.sys.clean\nsys\x00global\x00filter.lfs.process\ngit-lfs filter-process\x00" +
		"local\x00filter.evil.clean\n/tmp/evil\x00local\x00filter.evil.smudge\n/tmp/evil\x00worktree\x00filter.Odd.Name.process\nx\x00" +
		"command\x00filter.cli.clean\nx\x00local\x00filter.required\x00"
	overrides, err := parseFilterOverrides([]byte(scoped), true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"filter.evil.clean=", "filter.evil.smudge=", "filter.evil.process=", "filter.evil.required=false",
		"filter.Odd.Name.clean=", "filter.Odd.Name.smudge=", "filter.Odd.Name.process=", "filter.Odd.Name.required=false",
	}
	if !equalStrings(overrides, want) {
		t.Fatalf("unexpected scoped overrides: %#v", overrides)
	}
	overrides, err = parseFilterOverrides([]byte("filter.lfs.process\ngit-lfs\x00"), false)
	if err != nil || !equalStrings(overrides, []string{"filter.lfs.clean=", "filter.lfs.smudge=", "filter.lfs.process=", "filter.lfs.required=false"}) {
		t.Fatalf("unexpected unscoped overrides: %#v %v", overrides, err)
	}
	if _, err := parseFilterOverrides([]byte("local\x00filter.a=b.clean\nx\x00"), true); err == nil {
		t.Fatal("filter driver that cannot be overridden was accepted")
	}
}

// TestInspectionDoesNotRunRepositoryPrograms is the regression test for
// repository configuration executing programs as the agent user while Wio
// only inspects the repository.
func TestInspectionDoesNotRunRepositoryPrograms(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repository, markers := maliciousRepository(t, root)

	// Precondition: plain Git really runs the configured fsmonitor, so the
	// assertions below are not vacuous.
	touchLater(t, repository, "same.txt")
	if output, err := exec.Command("git", "-C", repository, "status", "--porcelain").CombinedOutput(); err != nil {
		t.Fatalf("plain git status: %v: %s", err, output)
	}
	if !markerExists(markers, "fsmonitor") || !markerExists(markers, "filter") {
		t.Fatal("test repository does not trigger fsmonitor and filter programs")
	}
	clearMarkers(t, markers)

	ctx := context.Background()
	roots := []string{root}
	touchLater(t, repository, "same.txt")
	if _, err := GetStatus(ctx, repository, roots); err != nil {
		t.Fatal(err)
	}
	touchLater(t, repository, "same.txt")
	if _, err := ListBranches(ctx, repository, roots); err != nil {
		t.Fatal(err)
	}
	if _, err := ListRemotes(ctx, repository, roots); err != nil {
		t.Fatal(err)
	}
	if _, err := ListCommits(ctx, repository, roots, CommitLogOptions{}); err != nil {
		t.Fatal(err)
	}
	touchLater(t, repository, "same.txt")
	changes, err := ListWorkspaceChanges(ctx, repository, roots)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "changed.txt" {
		t.Fatalf("unexpected changes: %#v", changes)
	}
	diff, err := ReadWorkspaceDiff(ctx, repository, "changed.txt", "", roots, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Content, "+after") {
		t.Fatalf("diff did not show raw content: %q", diff.Content)
	}
	if output, err := InspectOutput(ctx, repository, "status", "--porcelain"); err != nil {
		t.Fatalf("inspect status: %v: %s", err, output)
	}
	assertNoMarkers(t, markers)
}

func TestMutationsKeepHooksOnlyForOwnedRepositories(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repository, markers := maliciousRepository(t, root)
	ctx := context.Background()
	roots := []string{root}

	withCurrentUID(t, os.Geteuid()+1)
	if err := Stage(ctx, repository, []string{"changed.txt"}, false, roots); err != nil {
		t.Fatal(err)
	}
	if err := CommitChanges(ctx, repository, "foreign", roots); err != nil {
		t.Fatal(err)
	}
	assertNoMarkers(t, markers)

	currentUID = os.Geteuid
	writeGitChangeFile(t, repository, "changed.txt", "again\n")
	if err := Stage(ctx, repository, []string{"changed.txt"}, false, roots); err != nil {
		t.Fatal(err)
	}
	if err := CommitChanges(ctx, repository, "owned", roots); err != nil {
		t.Fatal(err)
	}
	if !markerExists(markers, "pre-commit") {
		t.Fatal("owned repository hook no longer runs on commit")
	}
}

// maliciousRepository creates a repository whose local config runs a marker
// program through fsmonitor, a filter driver, a diff textconv driver and a
// pre-commit hook.
func maliciousRepository(t *testing.T, root string) (string, string) {
	t.Helper()
	repository := filepath.Join(root, "repo")
	markers := filepath.Join(t.TempDir(), "markers")
	scripts := filepath.Join(t.TempDir(), "scripts")
	for _, directory := range []string{markers, scripts, repository} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := func(name, body string) string {
		path := filepath.Join(scripts, name)
		content := "#!/bin/sh\ntouch '" + filepath.Join(markers, name) + "'\n" + body + "\n"
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	runWriteGit(t, repository, "init", "-q", "-b", "main")
	runWriteGit(t, repository, "config", "user.name", "Wio Test")
	runWriteGit(t, repository, "config", "user.email", "wio@example.com")
	writeGitChangeFile(t, repository, ".gitattributes", "*.txt filter=evil diff=evil\n")
	writeGitChangeFile(t, repository, "same.txt", "same\n")
	writeGitChangeFile(t, repository, "changed.txt", "before\n")
	runWriteGit(t, repository, "add", ".")
	runWriteGit(t, repository, "commit", "-q", "-m", "base")

	runWriteGit(t, repository, "config", "core.fsmonitor", script("fsmonitor", "exit 1"))
	runWriteGit(t, repository, "config", "filter.evil.clean", script("filter", "cat"))
	runWriteGit(t, repository, "config", "filter.evil.smudge", script("filter", "cat"))
	runWriteGit(t, repository, "config", "filter.evil.required", "true")
	runWriteGit(t, repository, "config", "diff.evil.textconv", script("textconv", `cat "$1"`))
	runWriteGit(t, repository, "config", "diff.external", script("external", "exit 0"))
	runWriteGit(t, repository, "config", "core.hooksPath", scripts)
	script("pre-commit", "exit 0")
	writeGitChangeFile(t, repository, "changed.txt", "after\n")
	return repository, markers
}

var touchClock = time.Now().Add(-48 * time.Hour)

// touchLater changes the file's mtime without changing its content, which
// makes Git compare its content (through any clean filter) again.
func touchLater(t *testing.T, repository, name string) {
	t.Helper()
	touchClock = touchClock.Add(time.Minute)
	if err := os.Chtimes(filepath.Join(repository, name), touchClock, touchClock); err != nil {
		t.Fatal(err)
	}
}

func markerExists(markers, name string) bool {
	_, err := os.Stat(filepath.Join(markers, name))
	return err == nil
}

func clearMarkers(t *testing.T, markers string) {
	t.Helper()
	if err := os.RemoveAll(markers); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(markers, 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertNoMarkers(t *testing.T, markers string) {
	t.Helper()
	entries, err := os.ReadDir(markers)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("repository-configured program ran: %s", entry.Name())
	}
}

func withCurrentUID(t *testing.T, uid int) {
	t.Helper()
	previous := currentUID
	currentUID = func() int { return uid }
	t.Cleanup(func() { currentUID = previous })
}

func assertHardened(t *testing.T, args []string) {
	t.Helper()
	joined := strings.Join(args, "\x00")
	for _, value := range inspectionConfig {
		if !strings.Contains(joined, "-c\x00"+value) {
			t.Fatalf("missing hardening %q in %#v", value, args)
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "safe.directory") {
			t.Fatalf("safe.directory must never be passed: %#v", args)
		}
	}
}

func assertArgumentTail(t *testing.T, args []string, tail ...string) {
	t.Helper()
	if len(args) < len(tail) || !equalStrings(args[len(args)-len(tail):], tail) {
		t.Fatalf("arguments %#v do not end with %#v", args, tail)
	}
}
