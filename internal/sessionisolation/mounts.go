package sessionisolation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/sandbox"
)

func newTemporaryDirectory() (string, error) {
	root, err := os.MkdirTemp("", "wio-isolated-session-")
	if err != nil {
		return "", errors.New("cannot prepare private session temporary directory")
	}
	return root, nil
}

func removeTemporary(root string) { _ = os.RemoveAll(root) }

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func canonicalDirectory(path string) (string, error) {
	if !validPath(path) {
		return "", errors.New("invalid session filesystem scope")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !validPath(real) {
		return "", errors.New("session filesystem scope is unavailable")
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", errors.New("session filesystem scope must be a directory")
	}
	return real, nil
}

func runtimeMounts(cmd *exec.Cmd, paths []string, auth authentication, stateDir string, protectedPaths ...string) ([]sandbox.Mount, error) {
	mounts := sandbox.SystemMounts()
	for _, mount := range mounts {
		// The fixed system allowlist may resolve a public setting such as
		// resolv.conf to one regular file under /run. It never binds /run.
		if err := guardRuntimeCredentials(mount.Source, auth, stateDir, protectedPaths...); err != nil {
			return nil, err
		}
	}
	add := func(source, target string) error {
		if !validPath(source) || !validPath(target) {
			return errors.New("invalid public runtime path")
		}
		real, err := filepath.EvalSymlinks(source)
		if err != nil || !validPath(real) {
			return errors.New("required public CLI runtime is unavailable")
		}
		if err := guardRuntimeSource(real, auth, stateDir, protectedPaths...); err != nil {
			return err
		}
		mounts = append(mounts, sandbox.Mount{Source: real, Target: target})
		return nil
	}
	for _, path := range paths {
		if err := add(path, path); err != nil {
			return nil, err
		}
	}
	executable, err := filepath.Abs(cmd.Path)
	if err != nil {
		return nil, errors.New("cannot resolve isolated CLI executable")
	}
	real, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, errors.New("isolated CLI executable is unavailable")
	}
	if err := add(real, executable); err != nil {
		return nil, err
	}
	if real != executable {
		if err := add(real, real); err != nil {
			return nil, err
		}
	}
	// npm's CLI dependencies and optional native packages live under the same
	// public node_modules tree, which can be nested beneath Agent state.
	for parent := filepath.Dir(real); parent != "/" && parent != "."; parent = filepath.Dir(parent) {
		if filepath.Base(parent) == "node_modules" {
			if err := add(parent, parent); err != nil {
				return nil, err
			}
			break
		}
	}
	if node, err := exec.LookPath("node"); err == nil {
		if node, err = filepath.Abs(node); err == nil {
			if err := add(node, node); err != nil {
				return nil, err
			}
		}
	}
	return mounts, nil
}

func gitMounts(workspace string) ([]sandbox.Mount, error) {
	git := filepath.Join(workspace, ".git")
	info, err := os.Lstat(git)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot resolve workspace Git metadata")
	}
	if info.IsDir() {
		return nil, nil
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return nil, errors.New("invalid worktree Git metadata")
	}
	data, err := os.ReadFile(git)
	path, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if err != nil || !ok {
		return nil, errors.New("invalid worktree Git directory")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	gitDir, err := canonicalDirectory(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	common := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		path = strings.TrimSpace(string(data))
		if !filepath.IsAbs(path) {
			path = filepath.Join(gitDir, path)
		}
		if common, err = canonicalDirectory(filepath.Clean(path)); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot resolve worktree common directory")
	}
	// A worktree may only expand to Git metadata, never to a project root.
	if filepath.Base(common) != ".git" || !within(common, gitDir) {
		return nil, errors.New("worktree common directory is outside supported Git metadata")
	}
	if !within(filepath.Join(common, "worktrees"), gitDir) || gitDir == filepath.Join(common, "worktrees") {
		return nil, errors.New("worktree is not registered in its main repository")
	}
	backlink, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil || len(backlink) > 8192 {
		return nil, errors.New("worktree registration is unavailable")
	}
	registered := strings.TrimSpace(string(backlink))
	if !filepath.IsAbs(registered) {
		registered = filepath.Join(gitDir, registered)
	}
	registered, err = filepath.EvalSymlinks(registered)
	if err != nil || registered != git {
		return nil, errors.New("worktree Git pointer does not match its main repository registration")
	}
	return []sandbox.Mount{{Source: common, Target: common, Writable: true}}, nil
}

var persistentStateDirectories = map[string][]string{
	"codex":  {"sessions", "archived_sessions", "skills", "memories", "plugins", "shell_snapshots", "tmp", "log"},
	"claude": {"projects", "skills", "agents", "commands", "todos", "tasks", "plans", "plugins", "session-env"},
	"gemini": {"tmp", "skills", "agents", "commands", "extensions", "plans"},
}

func (m *Manager) stateMounts(auth authentication, root string, files map[string][]byte) ([]sandbox.Mount, error) {
	view := filepath.Join(root, "native-state")
	if err := os.Mkdir(view, 0700); err != nil {
		return nil, errors.New("cannot prepare isolated native state")
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(view, name), data, 0600); err != nil {
			return nil, errors.New("cannot prepare temporary CLI configuration")
		}
	}
	mounts := []sandbox.Mount{{Source: view, Target: auth.state, Writable: true}}
	for _, name := range persistentStateDirectories[auth.config.Provider] {
		path := filepath.Join(auth.state, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			// Only newly created state directories may receive the configured
			// account. Existing state and workspaces remain administrator-owned.
			if err := m.createNativeDirectory(path); err != nil {
				return nil, errors.New("cannot prepare native CLI history directory")
			}
		} else if err != nil {
			return nil, errors.New("native CLI history directory is unavailable")
		}
		real, err := canonicalDirectory(path)
		if err != nil || !within(auth.state, real) {
			return nil, errors.New("native CLI state symlink is outside its supported range")
		}
		mounts = append(mounts, sandbox.Mount{Source: real, Target: path, Writable: true})
	}
	return mounts, nil
}

// Assign only directories that this preparation actually creates. In
// particular, a fresh native-state parent must be traversable by the dropped
// account; an existing administrator-owned directory is never changed.
func (m *Manager) createNativeDirectory(path string) error {
	var missing []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("native CLI state parent must be a directory")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) || current == "/" {
			return errors.New("native CLI state parent is unavailable")
		}
		missing = append(missing, current)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			continue
		}
		if os.Geteuid() == 0 && m.options.UID != 0 {
			if err := os.Chown(missing[i], int(m.options.UID), int(m.options.GID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func addGitIdentity(ctx context.Context, home string, env environment) {
	for _, field := range []struct{ key, author, committer string }{{"user.name", "GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"}, {"user.email", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"}} {
		if env[field.author] != "" {
			continue
		}
		cmd := childprocess.CommandContext(childprocess.WithScope(ctx, childprocess.Git), "git", "config", "--file", filepath.Join(home, ".gitconfig"), "--no-includes", "--get", field.key)
		data, err := childprocess.Output(cmd)
		value := strings.TrimSpace(string(data))
		if err == nil && value != "" && !strings.ContainsAny(value, "\x00\r\n") {
			env[field.author], env[field.committer] = value, value
		}
	}
}
