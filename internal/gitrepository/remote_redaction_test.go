package gitrepository

import (
	"context"
	"strings"
	"testing"
)

func TestRedactRemoteURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://user:secret@github.com/team/repo.git":    "https://github.com/team/repo.git",
		"https://ghp_token@github.com/team/repo.git":      "https://github.com/team/repo.git",
		"https://oauth2:p%40ss@gitlab.example.com:8443/r": "https://gitlab.example.com:8443/r",
		"https://user:p@ss@example.com/repo":              "https://example.com/repo",
		"HTTP://user:secret@example.com":                  "HTTP://example.com",
		"https://user:secret@example.com?x=1":             "https://example.com?x=1",
		"https://example.com/team/@scope/repo.git":        "https://example.com/team/@scope/repo.git",
		"https://github.com/team/repo.git":                "https://github.com/team/repo.git",
		"ssh://git@github.com/team/repo.git":              "ssh://git@github.com/team/repo.git",
		"ssh://deploy:secret@example.com:2222/repo.git":   "ssh://deploy@example.com:2222/repo.git",
		"git+ssh://git:secret@example.com/repo.git":       "git+ssh://git@example.com/repo.git",
		"ssh://:secret@example.com/repo.git":              "ssh://example.com/repo.git",
		"git://user@example.com/repo.git":                 "git://example.com/repo.git",
		"git@github.com:team/repo.git":                    "git@github.com:team/repo.git",
		"deploy@example.com:team/repo.git":                "deploy@example.com:team/repo.git",
		"/srv/git/repo.git":                               "/srv/git/repo.git",
		"/srv/odd://user@name/repo":                       "/srv/odd://user@name/repo",
		"file:///srv/git/repo.git":                        "file:///srv/git/repo.git",
		"":                                                "",
	} {
		if got := RedactRemoteURL(raw); got != want {
			t.Errorf("RedactRemoteURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestListRemotesRedactsEmbeddedCredentials(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repository := createRepository(t, root, "repo")
	testGit(t, repository, "remote", "add", "origin", "https://deploy-user:synthetic-secret@example.com/team/repo.git")
	testGit(t, repository, "remote", "set-url", "--add", "--push", "origin", "https://synthetic-token@push.example.com/team/repo.git")
	testGit(t, repository, "remote", "add", "ssh", "ssh://git@example.com/team/repo.git")
	remotes, err := ListRemotes(context.Background(), repository, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Remote, len(remotes))
	for _, remote := range remotes {
		byName[remote.Name] = remote
		for _, value := range append(append([]string(nil), remote.FetchURLs...), remote.PushURLs...) {
			if strings.Contains(value, "synthetic") {
				t.Fatalf("remote %s reported credentials: %q", remote.Name, value)
			}
		}
	}
	if origin := byName["origin"]; !equalStrings(origin.FetchURLs, []string{"https://example.com/team/repo.git"}) || !equalStrings(origin.PushURLs, []string{"https://push.example.com/team/repo.git"}) {
		t.Fatalf("unexpected origin remote: %#v", origin)
	}
	if ssh := byName["ssh"]; !equalStrings(ssh.FetchURLs, []string{"ssh://git@example.com/team/repo.git"}) {
		t.Fatalf("unexpected ssh remote: %#v", ssh)
	}
}
