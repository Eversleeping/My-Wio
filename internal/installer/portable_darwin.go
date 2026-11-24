package installer

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/hostplatform"
	"os"
	"strings"
)

const launchdFile = "/Library/LaunchDaemons/com.wio.agent.plist"

func preparePortableService(ctx context.Context, l hostplatform.Layout) error {
	_, err := portableCommand(ctx, "sh", "-c", macAccountScript)
	if err != nil {
		return err
	}
	// A missing job is normal on first install. bootout waits for the old worker.
	_, _ = portableCommand(ctx, "launchctl", "bootout", "system/com.wio.agent")
	return nil
}
func protectPortableState(ctx context.Context, l hostplatform.Layout, state string) error {
	_, err := portableCommand(ctx, "chown", "-R", "_wio-agent:_wio-agent", state)
	if err != nil {
		return err
	}
	files := []string{l.Config, l.Key}
	if config, err := agent.LoadConfig(l.Config); err == nil && hostplatform.Contains(l.State, config.CodexAPIKeyFile) {
		files = append(files, config.CodexAPIKeyFile)
	}
	for _, file := range files {
		if _, err := os.Lstat(file); os.IsNotExist(err) {
			continue
		}
		if _, err := portableCommand(ctx, "chown", "_wio-agent:_wio-agent", file); err != nil {
			return err
		}
	}
	return nil
}
func startPortableService(ctx context.Context, l hostplatform.Layout) error {
	config, err := agent.LoadConfig(l.Config)
	if err != nil {
		return err
	}
	if err := atomicPortableWrite(launchdFile, []byte(macServiceDefinition(l, config.StateDir)), 0644); err != nil {
		return err
	}
	_, err = portableCommand(ctx, "launchctl", "bootstrap", "system", launchdFile)
	if err != nil {
		return err
	}
	_, err = portableCommand(ctx, "launchctl", "kickstart", "-k", "system/com.wio.agent")
	return err
}
func ensurePortableNode(ctx context.Context) error {
	if _, err := portableCommand(ctx, "node", "-e", "if(Number(process.versions.node.split('.')[0])<20)process.exit(1)"); err == nil {
		return nil
	}
	if _, err := portableCommand(ctx, "sh", "-c", macNodeScript); err != nil {
		return err
	}
	return os.Setenv("PATH", "/usr/local/bin:"+os.Getenv("PATH"))
}
func uninstallPortableLocal(ctx context.Context, l hostplatform.Layout) error {
	if _, err := portableCommand(ctx, "launchctl", "print", "system/com.wio.agent"); err == nil {
		if _, err := portableCommand(ctx, "launchctl", "bootout", "system/com.wio.agent"); err != nil {
			return err
		}
	}
	for _, name := range []string{launchdFile, l.Binary, l.Config, l.Key} {
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// State includes projects; explicit uninstall matches Linux's removal semantics.
	if err := os.RemoveAll(l.State); err != nil {
		return err
	}
	_, _ = portableCommand(ctx, "dscl", ".", "-delete", "/Users/_wio-agent")
	_, _ = portableCommand(ctx, "dscl", ".", "-delete", "/Groups/_wio-agent")

	return nil
}

func ensurePortableGit(ctx context.Context, _ string) error {
	if _, err := portableCommand(ctx, "xcode-select", "-p"); err == nil {
		if _, err := portableCommand(ctx, "git", "--version"); err == nil {
			return nil
		}
	}
	// Apple's signed Command Line Tools supply Git without relying on an
	// interactive Xcode installer or running Homebrew as root.
	marker := "/tmp/.com.apple.dt.CommandLineTools.installondemand.in-progress"
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		return err
	}
	defer os.Remove(marker)
	output, err := portableCommand(ctx, "softwareupdate", "--list")
	if err != nil {
		return err
	}
	label := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "* Label: Command Line Tools for Xcode-") {
			label = strings.TrimPrefix(line, "* Label: ")
		}
	}
	if label == "" {
		return errors.New("Apple Command Line Tools unavailable")
	}
	if _, err := portableCommand(ctx, "softwareupdate", "--install", label, "--verbose"); err != nil {
		return err
	}
	if _, err := portableCommand(ctx, "xcode-select", "--switch", "/Library/Developer/CommandLineTools"); err != nil {
		return err
	}
	_, err = portableCommand(ctx, "git", "--version")
	return err
}

func macServiceDefinition(l hostplatform.Layout, state string) string {
	escape := func(value string) string {
		var out bytes.Buffer
		_ = xml.EscapeText(&out, []byte(value))
		return out.String()
	}
	return strings.NewReplacer("__STATE__", escape(state), "__CONFIG__", escape(l.Config), "__BINARY__", escape(l.Binary)).Replace(macServicePlist)
}

func installPortableCLIs(ctx context.Context, state string, versions CLIVersions) ([]string, error) {
	command := func(ctx context.Context, name string, args ...string) (string, error) {
		base := []string{"-n", "-u", "_wio-agent", "env", "HOME=" + state, "PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin", name}
		// SSH usually starts in the login user's private home. npm calls getcwd
		// before processing --prefix, so use a directory the service user owns.
		return portableCommandInDirectory(ctx, state, "sudo", append(base, args...)...)
	}
	return installCLIs(ctx, state, versions, command)
}
