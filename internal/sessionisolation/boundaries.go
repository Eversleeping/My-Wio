package sessionisolation

import (
	"errors"
	"path/filepath"
)

// These host ranges must never replace bubblewrap's private process, device,
// IPC or temporary views, even when an administrator supplies runtime paths.
func privateSystemRange(path string) bool {
	for _, root := range []string{"/proc", "/sys", "/dev", "/run", "/var/run"} {
		if within(root, path) || within(path, root) {
			return true
		}
	}
	// Individual public executables and runtimes may be staged beneath /tmp.
	// Binding the entire host temporary directory would expose other leases.
	for _, root := range []string{"/tmp", "/var/tmp"} {
		if within(path, root) {
			return true
		}
	}
	return false
}

func credentialPaths(auth authentication, stateDir string, additional ...string) []string {
	paths := []string{"/etc/wio-agent"}
	paths = append(paths, additional...)
	for _, home := range []string{auth.home, stateDir} {
		if home == "" {
			continue
		}
		for _, name := range []string{".git-credentials", ".config/git/credentials", ".ssh", "codex.key", ".codex/auth.json", ".codex/config.toml", ".claude/.credentials.json", ".claude/settings.json", ".gemini/oauth_creds.json", ".gemini/settings.json"} {
			paths = append(paths, filepath.Join(home, name))
		}
	}
	if stateDir != "" {
		paths = append(paths, filepath.Join(stateDir, "claude", "api.json"), filepath.Join(stateDir, "gemini", "api.json"))
	}
	var out []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			continue
		}
		path = filepath.Clean(path)
		out = append(out, path)
		if real, err := filepath.EvalSymlinks(path); err == nil && real != path {
			out = append(out, real)
		}
	}
	return out
}

func guardRuntimeSource(source string, auth authentication, stateDir string, additional ...string) error {
	if privateSystemRange(source) {
		return errors.New("public runtime mounts must not expose host process, device, socket or temporary ranges")
	}
	return guardRuntimeCredentials(source, auth, stateDir, additional...)
}

func guardRuntimeCredentials(source string, auth authentication, stateDir string, additional ...string) error {
	for _, root := range []string{auth.home, auth.state, stateDir} {
		if root != "" && within(source, root) {
			return errors.New("public runtime mounts must not expose the Agent home or state")
		}
	}
	if auth.state != "" && within(auth.state, source) {
		return errors.New("public runtime mounts must not expose native account state")
	}
	for _, protected := range credentialPaths(auth, stateDir, additional...) {
		if within(source, protected) || within(protected, source) {
			return errors.New("public runtime mounts must not expose Agent credentials or privileged sockets")
		}
	}
	return nil
}

func guardWorkspace(workspace string, auth authentication, stateDir string, additional ...string) error {
	for _, root := range []string{"/proc", "/sys", "/dev", "/run", "/var/run"} {
		if within(root, workspace) || within(workspace, root) {
			return errors.New("session workspace must not expose a host process, device or socket range")
		}
	}
	for _, root := range []string{auth.home, auth.state, stateDir} {
		if root != "" && within(workspace, root) {
			return errors.New("session workspace must not contain Agent credentials or native account state")
		}
	}
	if auth.state != "" && within(auth.state, workspace) {
		return errors.New("native account state cannot be used as an isolated workspace")
	}
	for _, protected := range credentialPaths(auth, stateDir, additional...) {
		if within(workspace, protected) || within(protected, workspace) {
			return errors.New("session workspace must not expose Agent credentials or privileged sockets")
		}
	}
	return nil
}
