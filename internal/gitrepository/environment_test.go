package gitrepository

import (
	"context"
	"strings"
	"testing"
)

func TestGitCommandsDoNotInheritControlPlaneEnvironment(t *testing.T) {
	for _, name := range []string{"WIO_MASTER_KEY", "WIO_DATABASE_URL", "WIO_SETUP_TOKEN", "wio_private_setting"} {
		t.Setenv(name, "parent-only")
	}
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	cmd, err := gitCommand(context.Background(), "", gitInspect, "--version")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range cmd.Env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "WIO_") {
			t.Fatalf("inherited Wio setting reached Git: %s", name)
		}
	}
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("Git noninteractive override was lost")
	}
}
