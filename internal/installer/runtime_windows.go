package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/hostplatform"
)

// PrepareRuntime runs inside the worker, including after an update launched by
// an older immutable supervisor. Private Git avoids administrator-only PATHs
// and can repair a missing dependency without elevating the service account.
func PrepareRuntime(ctx context.Context, state, controlURL string) error {
	private := filepath.Join(state, "tools", "git")
	installed := filepath.Join(filepath.Dir(hostplatform.Current().Binary), "git")
	if err := os.Setenv("PATH", filepath.Join(private, "cmd")+";"+filepath.Join(installed, "cmd")+";"+os.Getenv("PATH")); err != nil {
		return err
	}
	for _, root := range []string{private, installed} {
		if _, err := portableCommand(ctx, filepath.Join(root, "cmd", "git.exe"), "--version"); err == nil {
			return nil
		}
	}
	if err := managedDirectory(filepath.Dir(private)); err != nil {
		return err
	}
	if err := installWindowsGit(ctx, private, strings.TrimRight(controlURL, "/")); err != nil {
		return err
	}
	if _, err := portableCommand(ctx, filepath.Join(private, "cmd", "git.exe"), "--version"); err != nil {
		return errors.New("private Git version verification failed")
	}
	return nil
}

func installWindowsGit(ctx context.Context, target, controlURL string) error {
	if err := managedDirectory(target); err != nil {
		return err
	}
	script := strings.NewReplacer("__WIO_GIT_DIR__", powershellQuote(target), "__WIO_GIT_SOURCE__", powershellQuote(windowsGitPackageURL(controlURL)), "__WIO_GIT_SHA256__", powershellQuote(WindowsGitSHA256)).Replace(windowsGitScript)
	command := childprocess.CommandContext(childprocess.WithScope(ctx, childprocess.Installation), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := childprocess.CombinedOutput(command)
	if err != nil {
		// This bootstrap contains only a public package URL and hash, no credentials.
		if len(output) > 2048 {
			output = output[:2048]
		}
		return fmt.Errorf("install verified Git package: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
