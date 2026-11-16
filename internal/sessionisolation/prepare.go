package sessionisolation

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/credentialproxy"
	"github.com/wio-platform/wio/internal/sandbox"
)

func reviewedDescriptors(cmd *exec.Cmd, env environment, provider string) ([]int, error) {
	reviewed := map[int]bool{}
	if provider == "claude" {
		if raw := env[claudeKeyDescriptor]; raw != "" {
			if fd, err := strconv.Atoi(raw); err == nil {
				reviewed[fd] = true
			}
		}
		for i, arg := range cmd.Args {
			if arg != "--mcp-config" || i+1 >= len(cmd.Args) {
				continue
			}
			for _, prefix := range []string{"/dev/fd/", "/proc/self/fd/"} {
				if raw, ok := strings.CutPrefix(cmd.Args[i+1], prefix); ok {
					if fd, err := strconv.Atoi(raw); err == nil {
						reviewed[fd] = true
					}
				}
			}
		}
	}
	out := make([]int, len(cmd.ExtraFiles))
	for i, file := range cmd.ExtraFiles {
		if file == nil || !reviewed[3+i] {
			return nil, errors.New("unreviewed inherited CLI descriptor")
		}
		out[i] = 3 + i
	}
	if len(reviewed) != len(out) {
		return nil, errors.New("invalid inherited CLI descriptor reference")
	}
	return out, nil
}

func (m *Manager) Prepare(ctx context.Context, cmd *exec.Cmd) (release func(), err error) {
	scope := childprocess.EnvironmentScope(ctx)
	provider := ""
	switch scope {
	case childprocess.Codex:
		provider = "codex"
	case childprocess.Claude:
		provider = "claude"
	case childprocess.Gemini:
		provider = "gemini"
	default:
		return func() {}, nil
	}
	if !m.config.Enabled {
		return func() {}, nil
	}
	status := m.Status(ctx)
	if status.Mode != "enabled" {
		return nil, errors.New("session isolation unavailable: " + status.Reason)
	}
	original := commandEnvironment(cmd.Env)
	descriptors, err := reviewedDescriptors(cmd, original, provider)
	if err != nil {
		return nil, err
	}
	versionQuery := len(cmd.Args) == 2 && cmd.Args[1] == "--version"
	auth := authentication{config: credentialproxy.Config{Provider: provider}, home: original["HOME"], state: filepath.Join(original["HOME"], "."+provider), descriptor: -1}
	if versionQuery {
		if !validPath(auth.home) {
			return nil, errors.New("session isolation requires an absolute CLI home")
		}
	} else {
		auth, err = parseAuthentication(provider, original, cmd)
		if err != nil {
			return nil, err
		}
	}
	root, err := newTemporaryDirectory()
	if err != nil {
		return nil, err
	}
	var lease *credentialproxy.Lease
	var boundaryRelease func()
	var files []*os.File
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if lease != nil {
				lease.Close()
			}
			if boundaryRelease != nil {
				boundaryRelease()
			}
			for _, file := range files {
				_ = file.Close()
			}
			removeTemporary(root)
		})
	}
	release = cleanup
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	env := publicEnvironment(original, provider)
	configuration := map[string][]byte{}
	if !versionQuery {
		lease, err = credentialproxy.Start(auth.config)
		if err != nil {
			return nil, err
		}
		addGitIdentity(ctx, auth.home, env)
		configuration, err = auth.leaseConfiguration(lease, env)
		if err != nil {
			return nil, err
		}
	}
	if script := original["WIO_GEMINI_STEER_SCRIPT"]; script != "" && TempDir(ctx) != "" && within(TempDir(ctx), script) {
		env["WIO_GEMINI_STEER_SCRIPT"] = script
	}
	if auth.descriptor >= 0 {
		file, descriptorErr := sandbox.DescriptorFile([]byte(lease.Token()))
		if descriptorErr != nil {
			return nil, errors.New("cannot prepare temporary Claude authentication")
		}
		files = append(files, file)
		cmd.ExtraFiles[auth.descriptor] = file
	}
	mounts, err := runtimeMounts(cmd, m.config.RuntimePaths, auth, m.stateDir, m.protectedPaths...)
	if err != nil {
		return nil, err
	}
	privateHome := filepath.Join(root, "home")
	if err = os.Mkdir(privateHome, 0700); err != nil {
		return nil, errors.New("cannot prepare private CLI home")
	}
	if provider == "claude" && !versionQuery {
		// A private directory allows Claude's atomic replacement of this file.
		// Binding the file itself would make rename fail with EBUSY.
		if err = os.WriteFile(filepath.Join(privateHome, ".claude.json"), configuration[".claude.json"], 0600); err != nil {
			return nil, errors.New("cannot prepare private Claude account settings")
		}
	}
	env["HOME"] = auth.home
	mounts = append(mounts, sandbox.Mount{Source: privateHome, Target: auth.home, Writable: true})
	if !versionQuery {
		native, stateErr := m.stateMounts(auth, root, configuration)
		if stateErr != nil {
			return nil, stateErr
		}
		mounts = append(mounts, native...)
	}
	mounts = append(mounts, sandbox.Mount{Source: root, Target: root, Writable: true})
	workspace := Workspace(ctx)
	if workspace == "" {
		workspace = cmd.Dir
	}
	if workspace != "" {
		workspace, err = canonicalDirectory(filepath.Clean(workspace))
		if err != nil {
			return nil, err
		}
		if err = guardWorkspace(workspace, auth, m.stateDir, m.protectedPaths...); err != nil {
			return nil, err
		}
		mounts = append(mounts, sandbox.Mount{Source: workspace, Target: workspace, Writable: true})
		git, gitErr := gitMounts(workspace)
		if gitErr != nil {
			return nil, gitErr
		}
		mounts = append(mounts, git...)
	} else {
		workspace = root
	}
	if temp := TempDir(ctx); temp != "" {
		mounts = append(mounts, sandbox.Mount{Source: temp, Target: temp, Writable: true})
	}
	readPaths, _ := ctx.Value(readPathsKey{}).([]string)
	for index, path := range readPaths {
		if path == "" {
			continue
		}
		real, readErr := filepath.EvalSymlinks(path)
		if readErr != nil || !validPath(real) || !within(filepath.Join(m.stateDir, "claude", "forks"), real) {
			return nil, errors.New("unreviewed CLI snapshot path")
		}
		info, readErr := os.Stat(real)
		if readErr != nil || !info.Mode().IsRegular() {
			return nil, errors.New("CLI snapshot is unavailable")
		}
		// Snapshots are created by the trusted Agent. Copy them into this
		// process's private range rather than changing the original owner.
		copyPath := filepath.Join(root, "snapshot-"+strconv.Itoa(index)+".jsonl")
		if err = copySnapshot(real, copyPath); err != nil {
			return nil, err
		}
		mounts = append(mounts, sandbox.Mount{Source: copyPath, Target: path})
	}
	if err = m.prepareTemporaryOwnership(root); err != nil {
		return nil, err
	}
	if temp := TempDir(ctx); temp != "" {
		if err = m.prepareTemporaryOwnership(temp); err != nil {
			return nil, err
		}
	}
	env["TMPDIR"], env["TMP"], env["TEMP"] = root, root, root
	cmd.Dir, cmd.Env = workspace, env.entries()
	boundaryRelease, err = sandbox.Prepare(cmd, sandbox.Policy{Mounts: mounts, WorkDir: workspace, Descriptors: descriptors}, m.options)
	if err != nil {
		return nil, err
	}
	return release, nil
}

func copySnapshot(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return errors.New("cannot read trusted CLI snapshot")
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("cannot prepare private CLI snapshot")
	}
	defer out.Close()
	const limit = 128 << 20
	size, err := io.Copy(out, io.LimitReader(in, limit+1))
	if err != nil || size > limit {
		return errors.New("trusted CLI snapshot is unavailable or too large")
	}
	return nil
}

func (m *Manager) prepareTemporaryOwnership(root string) error {
	if os.Geteuid() != 0 || m.options.UID == 0 {
		return nil
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("private session temporary range contains a symlink")
		}
		return os.Chown(path, int(m.options.UID), int(m.options.GID))
	})
	if err != nil {
		return errors.New("cannot prepare private temporary data for the unprivileged session account")
	}
	return nil
}
