package gitrepository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCreateEmptyRepository(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	target := filepath.Join(root, "empty")

	result, err := Create(context.Background(), CreateOptions{Path: target, InitialBranch: "trunk"}, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	resultInfo, statErr := os.Stat(result.Path)
	targetInfo, targetStatErr := os.Stat(target)
	if statErr != nil || targetStatErr != nil || !os.SameFile(resultInfo, targetInfo) || result.Branch != "trunk" || result.Head != "" || !result.Unborn {
		t.Fatalf("unexpected create result: %#v", result)
	}
	status, err := GetStatus(context.Background(), target, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "trunk" || !status.Unborn || status.Detached || status.Dirty {
		t.Fatalf("unexpected empty status: %#v", status)
	}
	page, err := ListCommits(context.Background(), target, []string{root}, CommitLogOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Commits) != 0 || page.HasMore || page.Limit != 10 {
		t.Fatalf("unexpected empty commit page: %#v", page)
	}
	branches, err := ListBranches(context.Background(), target, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0].Name != "trunk" || branches[0].Kind != "local" || !branches[0].Current || branches[0].CommitSHA != "" {
		t.Fatalf("unexpected unborn branch list: %#v", branches)
	}
	explicitHead, err := ListCommits(context.Background(), target, []string{root}, CommitLogOptions{Ref: "HEAD", Limit: 10})
	if err != nil || len(explicitHead.Commits) != 0 {
		t.Fatalf("unexpected explicit HEAD page for unborn repository: %#v %v", explicitHead, err)
	}
}

func TestCreateWithRemoteAndREADME(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	target := filepath.Join(root, "service")
	remote := "https://example.com/team/service.git"

	result, err := Create(context.Background(), CreateOptions{
		Path:             target,
		RemoteURL:        remote,
		InitializeREADME: true,
		READMEContent:    "# Service\n",
		CommitMessage:    "bootstrap repository",
		AuthorName:       "Wio Test",
		AuthorEmail:      "wio@example.com",
	}, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Branch != "main" || result.Unborn || result.Head == "" || result.RemoteURL != remote {
		t.Fatalf("unexpected create result: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(target, "README.md"))
	if err != nil || string(content) != "# Service\n" {
		t.Fatalf("unexpected README: %q %v", content, err)
	}
	remotes, err := ListRemotes(context.Background(), target, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 1 || remotes[0].Name != "origin" || !equalStrings(remotes[0].FetchURLs, []string{remote}) || !equalStrings(remotes[0].PushURLs, []string{remote}) {
		t.Fatalf("unexpected remotes: %#v", remotes)
	}
	page, err := ListCommits(context.Background(), target, []string{root}, CommitLogOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Commits) != 1 || page.Commits[0].Title != "bootstrap repository" || page.Commits[0].AuthorName != "Wio Test" || page.Commits[0].AuthorEmail != "wio@example.com" || len(page.Commits[0].Parents) != 0 {
		t.Fatalf("unexpected initial commit: %#v", page)
	}
}

func TestCreateRejectsUnsafePathsAndRemoteURLs(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	outside := t.TempDir()
	tests := []struct {
		name    string
		options CreateOptions
	}{
		{name: "relative path", options: CreateOptions{Path: "repo"}},
		{name: "outside root", options: CreateOptions{Path: filepath.Join(outside, "repo")}},
		{name: "invalid branch", options: CreateOptions{Path: filepath.Join(root, "bad-branch"), InitialBranch: "bad branch"}},
		{name: "HTTP credentials", options: CreateOptions{Path: filepath.Join(root, "http-user"), RemoteURL: "https://user:secret@example.com/repo.git"}},
		{name: "SSH password", options: CreateOptions{Path: filepath.Join(root, "ssh-password"), RemoteURL: "ssh://git:secret@example.com/repo.git"}},
		{name: "external helper", options: CreateOptions{Path: filepath.Join(root, "external-helper"), RemoteURL: "ext::command"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Create(context.Background(), test.options, []string{root}); err == nil {
				t.Fatal("expected input to be rejected")
			}
			if filepath.IsAbs(test.options.Path) && strings.HasPrefix(test.options.Path, root) {
				if _, err := os.Stat(test.options.Path); !os.IsNotExist(err) {
					t.Fatalf("failed creation left a directory behind: %v", err)
				}
			}
		})
	}
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), CreateOptions{Path: existing}, []string{root}); err == nil {
		t.Fatal("expected existing path rejection")
	}
	for name, remote := range map[string]string{
		"ssh URL": "ssh://git@example.com/team/repo.git",
		"SCP URL": "git@example.com:team/repo.git",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := Create(context.Background(), CreateOptions{Path: filepath.Join(root, strings.ToLower(strings.ReplaceAll(name, " ", "-"))), RemoteURL: remote}, []string{root})
			if err != nil {
				t.Fatal(err)
			}
			if result.RemoteURL != remote {
				t.Fatalf("remote was not retained: %#v", result)
			}
		})
	}
}

func TestCreateAndQueriesRejectSymlinkEscapeAndNestedRepositoryPath(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlinks requires additional Windows privileges: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), CreateOptions{Path: filepath.Join(link, "repo")}, []string{root}); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}

	repository := createRepository(t, root, "repo")
	nested := filepath.Join(repository, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := GetStatus(context.Background(), nested, []string{root}); err == nil {
		t.Fatal("expected nested repository path to be rejected")
	}
	if _, err := GetStatus(context.Background(), repository, []string{outside}); err == nil {
		t.Fatal("expected repository outside managed roots to be rejected")
	}
}

func TestStatusReportsDivergenceChangesAndDetachedHead(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	root := filepath.Join(base, "managed")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(base, "remote.git")
	testGit(t, base, "init", "--bare", bare)
	seed := filepath.Join(base, "seed")
	testGit(t, base, "clone", bare, seed)
	configureIdentity(t, seed)
	writeFile(t, filepath.Join(seed, "tracked.txt"), "initial\n")
	testGit(t, seed, "add", "--", "tracked.txt")
	testGit(t, seed, "commit", "-m", "initial")
	testGit(t, seed, "branch", "-M", "main")
	testGit(t, seed, "push", "-u", "origin", "main")
	testGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	repository := filepath.Join(root, "repo")
	testGit(t, root, "clone", bare, repository)
	configureIdentity(t, repository)
	writeFile(t, filepath.Join(repository, "local.txt"), "local\n")
	testGit(t, repository, "add", "--", "local.txt")
	testGit(t, repository, "commit", "-m", "local commit")

	other := filepath.Join(base, "other")
	testGit(t, base, "clone", bare, other)
	configureIdentity(t, other)
	writeFile(t, filepath.Join(other, "remote.txt"), "remote\n")
	testGit(t, other, "add", "--", "remote.txt")
	testGit(t, other, "commit", "-m", "remote commit")
	testGit(t, other, "push", "origin", "main")
	testGit(t, repository, "fetch", "origin")

	writeFile(t, filepath.Join(repository, "tracked.txt"), "staged\n")
	testGit(t, repository, "add", "--", "tracked.txt")
	writeFile(t, filepath.Join(repository, "tracked.txt"), "unstaged after staging\n")
	writeFile(t, filepath.Join(repository, "untracked.txt"), "new\n")

	status, err := GetStatus(context.Background(), repository, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" || status.Detached || status.Unborn || status.Head == "" || status.Upstream != "origin/main" || status.Ahead != 1 || status.Behind != 1 || status.Staged != 1 || status.Unstaged != 1 || status.Untracked != 1 || !status.Dirty {
		t.Fatalf("unexpected divergent dirty status: %#v", status)
	}

	testGit(t, repository, "reset", "--hard", "HEAD")
	if err := os.Remove(filepath.Join(repository, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "checkout", "--detach", "HEAD")
	status, err = GetStatus(context.Background(), repository, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Detached || status.Branch != "" || status.Head == "" || status.Upstream != "" || status.Ahead != 0 || status.Behind != 0 || status.Dirty {
		t.Fatalf("unexpected detached status: %#v", status)
	}
}

func TestListBranchesAndRemotes(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	root := filepath.Join(base, "managed")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(base, "remote.git")
	testGit(t, base, "init", "--bare", bare)
	repository := createRepository(t, root, "repo")
	testGit(t, repository, "branch", "feature/test")
	testGit(t, repository, "remote", "add", "origin", fileURL(bare))
	testGit(t, repository, "push", "-u", "origin", "main")
	testGit(t, repository, "fetch", "origin")
	testGit(t, repository, "remote", "add", "backup", "https://example.com/team/repo.git")
	testGit(t, repository, "remote", "set-url", "--add", "backup", "https://mirror.example.com/team/repo.git")
	testGit(t, repository, "remote", "set-url", "--add", "--push", "backup", "https://push-one.example.com/team/repo.git")
	testGit(t, repository, "remote", "set-url", "--add", "--push", "backup", "https://push-two.example.com/team/repo.git")

	branches, err := ListBranches(context.Background(), repository, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Branch, len(branches))
	for _, branch := range branches {
		byName[branch.Name] = branch
	}
	if main := byName["main"]; main.Kind != "local" || !main.Current || main.Upstream != "origin/main" || main.CommitSHA == "" {
		t.Fatalf("unexpected main branch: %#v", main)
	}
	if feature := byName["feature/test"]; feature.Kind != "local" || feature.Current || feature.CommitSHA == "" {
		t.Fatalf("unexpected feature branch: %#v", feature)
	}
	if remoteMain := byName["origin/main"]; remoteMain.Kind != "remote" || remoteMain.CommitSHA == "" {
		t.Fatalf("unexpected remote main branch: %#v", remoteMain)
	}

	remotes, err := ListRemotes(context.Background(), repository, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	byRemote := make(map[string]Remote, len(remotes))
	for _, remote := range remotes {
		byRemote[remote.Name] = remote
	}
	if origin := byRemote["origin"]; !equalStrings(origin.FetchURLs, []string{fileURL(bare)}) || !equalStrings(origin.PushURLs, []string{fileURL(bare)}) {
		t.Fatalf("unexpected origin remote: %#v", origin)
	}
	backup := byRemote["backup"]
	if !equalStrings(backup.FetchURLs, []string{"https://example.com/team/repo.git", "https://mirror.example.com/team/repo.git"}) || !equalStrings(backup.PushURLs, []string{"https://push-one.example.com/team/repo.git", "https://push-two.example.com/team/repo.git"}) {
		t.Fatalf("unexpected backup remote: %#v", backup)
	}
}

func TestListCommitsPaginatesAndReturnsStructuredMetadata(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repository := createRepository(t, root, "repo")
	for index := 2; index <= 5; index++ {
		writeFile(t, filepath.Join(repository, "history.txt"), strings.Repeat("x", index))
		testGitWithEnv(t, repository, []string{
			"GIT_AUTHOR_DATE=2026-07-" + twoDigits(index) + "T10:00:00+08:00",
			"GIT_COMMITTER_DATE=2026-07-" + twoDigits(index) + "T10:00:00+08:00",
		}, "add", "--", "history.txt")
		testGitWithEnv(t, repository, []string{
			"GIT_AUTHOR_DATE=2026-07-" + twoDigits(index) + "T10:00:00+08:00",
			"GIT_COMMITTER_DATE=2026-07-" + twoDigits(index) + "T10:00:00+08:00",
		}, "commit", "-m", "commit "+twoDigits(index))
	}

	first, err := ListCommits(context.Background(), repository, []string{root}, CommitLogOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Commits) != 2 || !first.HasMore || first.NextOffset != 2 || first.Commits[0].Title != "commit 05" || first.Commits[1].Title != "commit 04" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	if first.Commits[0].AuthorName != "Wio Test" || first.Commits[0].AuthorEmail != "wio@example.com" || first.Commits[0].AuthoredAt.Format(time.RFC3339) != "2026-07-05T10:00:00+08:00" || len(first.Commits[0].Parents) != 1 {
		t.Fatalf("unexpected structured commit: %#v", first.Commits[0])
	}
	second, err := ListCommits(context.Background(), repository, []string{root}, CommitLogOptions{Offset: first.NextOffset, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Commits) != 2 || !second.HasMore || second.NextOffset != 4 || second.Commits[0].Title != "commit 03" || second.Commits[1].Title != "commit 02" {
		t.Fatalf("unexpected second page: %#v", second)
	}
	last, err := ListCommits(context.Background(), repository, []string{root}, CommitLogOptions{Offset: second.NextOffset, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Commits) != 1 || last.HasMore || last.NextOffset != 0 || last.Commits[0].Title != "initial" || len(last.Commits[0].Parents) != 0 {
		t.Fatalf("unexpected last page: %#v", last)
	}
	if _, err := ListCommits(context.Background(), repository, []string{root}, CommitLogOptions{Limit: maxCommitLimit + 1}); err == nil {
		t.Fatal("expected excessive limit rejection")
	}
	if _, err := ListCommits(context.Background(), repository, []string{root}, CommitLogOptions{Offset: -1}); err == nil {
		t.Fatal("expected negative offset rejection")
	}
	if _, err := ListCommits(context.Background(), repository, []string{root}, CommitLogOptions{Ref: "--all"}); err == nil {
		t.Fatal("expected option-shaped reference rejection")
	}
}

func TestOutputParsersRejectMalformedData(t *testing.T) {
	validStatusHeaders := "# branch.oid 0123456789012345678901234567890123456789\n# branch.head main\n"
	statusCases := []string{
		"",
		"# branch.oid 0123456789012345678901234567890123456789\n",
		"# branch.oid (initial)\n# branch.head (detached)\n",
		validStatusHeaders + "# branch.ab ahead behind\n",
		validStatusHeaders + "# branch.ab +1 -oops\n",
		validStatusHeaders + "1 malformed\n",
	}
	for _, output := range statusCases {
		if _, err := parseStatus([]byte(output)); err == nil {
			t.Fatalf("expected malformed status to fail: %q", output)
		}
	}
	if _, err := parseBranches([]byte("refs/heads/main\x00main\x00only-three-fields\n")); err == nil {
		t.Fatal("expected malformed branch output to fail")
	}
	validOID := "0123456789012345678901234567890123456789"
	if _, err := parseBranches([]byte("refs/heads/main\x00\x00" + validOID + "\x00\x00*\x00\n")); err == nil {
		t.Fatal("expected empty branch name to fail")
	}
	if _, err := parseCommits([]byte("sha\x00name\x00email\x00date\x00title\x00")); err == nil {
		t.Fatal("expected incomplete commit output to fail")
	}
	if _, err := parseCommits([]byte(validOID + "\x00name\x00email\x00not-a-time\x00title\x00\x00")); err == nil {
		t.Fatal("expected invalid commit time to fail")
	}
	if _, err := parseCommits([]byte(validOID + "\x00name\x00email\x002026-07-20T10:00:00Z\x00title\x00bad-parent\x00")); err == nil {
		t.Fatal("expected invalid parent commit to fail")
	}
}

func createRepository(t *testing.T, root, name string) string {
	t.Helper()
	repository := filepath.Join(root, name)
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "init", "-b", "main")
	configureIdentity(t, repository)
	writeFile(t, filepath.Join(repository, "README.md"), "initial\n")
	testGit(t, repository, "add", "--", "README.md")
	testGitWithEnv(t, repository, []string{
		"GIT_AUTHOR_DATE=2026-07-01T10:00:00+08:00",
		"GIT_COMMITTER_DATE=2026-07-01T10:00:00+08:00",
	}, "commit", "-m", "initial")
	return repository
}

func configureIdentity(t *testing.T, repository string) {
	t.Helper()
	testGit(t, repository, "config", "user.name", "Wio Test")
	testGit(t, repository, "config", "user.email", "wio@example.com")
	testGit(t, repository, "config", "commit.gpgsign", "false")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	testGitWithEnv(t, directory, nil, args...)
}

func testGitWithEnv(t *testing.T, directory string, environment []string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), environment...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func fileURL(path string) string {
	return "file:///" + strings.TrimPrefix(filepath.ToSlash(path), "/")
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
