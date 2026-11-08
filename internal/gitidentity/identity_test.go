package gitidentity

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationProtectsAndIncludesCommitIdentity(t *testing.T) {
	configuration, err := Configuration("Example User", "user@users.noreply.github.com", "/var/lib/wio-agent/.git-credentials")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`name = "Example User"`, `email = "user@users.noreply.github.com"`, `helper = "store --file='/var/lib/wio-agent/.git-credentials'"`} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("configuration missing %q:\n%s", expected, configuration)
		}
	}
}

func TestNormalizeRejectsInvalidCommitIdentity(t *testing.T) {
	for _, identity := range [][2]string{{"", "user@example.com"}, {"Example User", "not-an-email"}, {"Bad\nName", "user@example.com"}, {"Bad\tName", "user@example.com"}, {"Bad\x7fName", "user@example.com"}, {"Bad\u0085Name", "user@example.com"}, {"Bad\xffName", "user@example.com"}} {
		if _, _, err := Normalize(identity[0], identity[1]); err == nil {
			t.Fatalf("invalid identity was accepted: %#v", identity)
		}
	}
}

// TestConfigurationRoundTripsThroughGit writes the generated file and reads it
// back with Git: Go escapes such as \u200d used to make every Git command on
// the server fail with "bad config line".
func TestConfigurationRoundTripsThroughGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	state := filepath.Join(root, `Agent's "state" $HOME \dir`)
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	credentials := filepath.Join(state, ".git-credentials")
	if err := os.WriteFile(credentials, []byte("https://synthetic-user:synthetic-secret@git.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "Dev 👩\u200d💻 O'Neil \"Q\" \\ 测试 #;"
	email := "dev@example.test"
	configuration, err := Configuration(name, email, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configuration, `\u`) || strings.Contains(configuration, `\x`) {
		t.Fatalf("configuration contains escapes Git cannot parse:\n%s", configuration)
	}
	file := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(file, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(input string, args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = isolatedGitEnvironment(root, file)
		command.Stdin = strings.NewReader(input)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSuffix(string(output), "\n")
	}
	if got := git("", "config", "--file", file, "--get", "user.name"); got != name {
		t.Fatalf("user.name = %q, want %q", got, name)
	}
	if got := git("", "config", "--file", file, "--get", "user.email"); got != email {
		t.Fatalf("user.email = %q, want %q", got, email)
	}
	// The helper runs through the shell, so the quoted path must survive
	// spaces, quotes, dollars and backslashes.
	filled := git("protocol=https\nhost=git.example.test\n\n", "credential", "fill")
	if !strings.Contains(filled, "password=synthetic-secret") {
		t.Fatalf("credential helper did not read %s:\n%s", credentials, filled)
	}
}

func TestQuoteConfigValueRejectsControlCharacters(t *testing.T) {
	for _, value := range []string{"line\nbreak", "tab\there", "nul\x00", "bell\a"} {
		if _, err := quoteConfigValue(value); err == nil {
			t.Fatalf("control character accepted: %q", value)
		}
	}
	quoted, err := quoteConfigValue(`a"b\c 👍`)
	if err != nil || quoted != `"a\"b\\c 👍"` {
		t.Fatalf("quoteConfigValue = %q, %v", quoted, err)
	}
}

// isolatedGitEnvironment uses only the generated file as global config.
func isolatedGitEnvironment(home, global string) []string {
	environment := []string{"HOME=" + home, "GIT_CONFIG_GLOBAL=" + global, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PATH" {
			environment = append(environment, entry)
		}
	}
	return environment
}
