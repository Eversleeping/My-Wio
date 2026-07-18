package gitidentity

import (
	"strings"
	"testing"
)

func TestConfigurationProtectsAndIncludesCommitIdentity(t *testing.T) {
	configuration, err := Configuration("Example User", "user@users.noreply.github.com", "/var/lib/wio-agent/.git-credentials")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`name = "Example User"`, `email = "user@users.noreply.github.com"`, `helper = store --file="/var/lib/wio-agent/.git-credentials"`} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("configuration missing %q:\n%s", expected, configuration)
		}
	}
}

func TestNormalizeRejectsInvalidCommitIdentity(t *testing.T) {
	for _, identity := range [][2]string{{"", "user@example.com"}, {"Example User", "not-an-email"}, {"Bad\nName", "user@example.com"}} {
		if _, _, err := Normalize(identity[0], identity[1]); err == nil {
			t.Fatalf("invalid identity was accepted: %#v", identity)
		}
	}
}
