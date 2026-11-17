// Package sessionisolation binds one official CLI process to a fixed workspace
// and a revocable inference lease. Agent helpers retain their original scope.
package sessionisolation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sandbox"
)

type Config struct {
	Enabled      bool     `json:"enabled"`
	UID          uint32   `json:"uid,omitempty"`
	GID          uint32   `json:"gid,omitempty"`
	Bwrap        string   `json:"bubblewrap,omitempty"`
	RuntimePaths []string `json:"runtime_paths,omitempty"`
}

func (c Config) Validate() error {
	if len(c.RuntimePaths) > 32 {
		return errors.New("too many session isolation runtime paths")
	}
	for _, path := range c.RuntimePaths {
		if !validPath(path) {
			return errors.New("session isolation runtime paths must be absolute subdirectories or files")
		}
	}
	return nil
}

type Status = protocol.SessionIsolationStatus

type Manager struct {
	config         Config
	stateDir       string
	protectedPaths []string
	options        sandbox.Options
	mu             sync.Mutex
	checked        time.Time
	status         Status
}

func New(config *Config, stateDir string, protectedPaths ...string) *Manager {
	m := &Manager{stateDir: stateDir, protectedPaths: append([]string(nil), protectedPaths...), status: Status{Mode: "disabled"}}
	if config != nil {
		m.config = *config
		m.config.RuntimePaths = append([]string(nil), config.RuntimePaths...)
		m.options = sandbox.Options{Bwrap: config.Bwrap, UID: config.UID, GID: config.GID}
	}
	return m
}

type managerKey struct{}
type workspaceKey struct{}
type temporaryKey struct{}
type readPathsKey struct{}

func (m *Manager) Context(ctx context.Context) context.Context {
	if m == nil || !m.config.Enabled {
		return ctx
	}
	ctx = context.WithValue(ctx, managerKey{}, m)
	return childprocess.WithCommandPreparer(ctx, m.Prepare)
}

func Enabled(ctx context.Context) bool {
	m, _ := ctx.Value(managerKey{}).(*Manager)
	return m != nil && m.config.Enabled
}

func WithWorkspace(ctx context.Context, workspace string) context.Context {
	return context.WithValue(ctx, workspaceKey{}, workspace)
}

// PrepareGeminiHome runs before the adapter writes its trusted settings. With
// a root Agent, otherwise those writes can create a root-only parent before
// Prepare has a chance to create the dropped account's native history tree.
// Existing administrator-owned directories are never reassigned.
func PrepareGeminiHome(ctx context.Context, environment []string) error {
	m, _ := ctx.Value(managerKey{}).(*Manager)
	if m == nil || !m.config.Enabled {
		return nil
	}
	env := commandEnvironment(environment)
	home := env.first("GEMINI_CLI_HOME", "HOME")
	if !validPath(home) {
		return errors.New("session isolation requires an absolute Gemini home")
	}
	return m.createNativeDirectory(filepath.Join(home, ".gemini"))
}

func Workspace(ctx context.Context) string {
	workspace, _ := ctx.Value(workspaceKey{}).(string)
	return workspace
}

func ResolveWorkspace(path string) (string, error) { return canonicalDirectory(filepath.Clean(path)) }

func TempDir(ctx context.Context) string {
	path, _ := ctx.Value(temporaryKey{}).(string)
	return path
}

// Temporary creates a private range before adapters prepare images or loaders.
// With isolation disabled the adapter continues using its original temp path.
func Temporary(ctx context.Context) (context.Context, func(), error) {
	if !Enabled(ctx) || TempDir(ctx) != "" {
		return ctx, func() {}, nil
	}
	root, err := newTemporaryDirectory()
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, temporaryKey{}, root), func() { removeTemporary(root) }, nil
}

// WithReadPaths is used only after trusted snapshot preparation. It does not
// let a CLI request a new mount or expand a running process's workspace.
func WithReadPaths(ctx context.Context, paths ...string) context.Context {
	old, _ := ctx.Value(readPathsKey{}).([]string)
	out := append([]string(nil), old...)
	for _, path := range paths {
		if path == "" {
			continue
		}
		found := false
		for _, previous := range out {
			found = found || previous == path
		}
		if !found {
			out = append(out, path)
		}
	}
	return context.WithValue(ctx, readPathsKey{}, out)
}

func validPath(path string) bool {
	return filepath.IsAbs(path) && path != "/" && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}

func (m *Manager) Status(ctx context.Context) Status {
	if m == nil || !m.config.Enabled {
		return Status{Mode: "disabled"}
	}
	if err := m.checkSupport(ctx, false); err != nil {
		return Status{Mode: "unsupported", Reason: err.Error()}
	}
	return Status{Mode: "enabled"}
}

// CheckSupport tests the complete boundary even while isolation is disabled.
// It does not change configuration or launch an official CLI.
func (m *Manager) CheckSupport(ctx context.Context) error {
	return m.checkSupport(ctx, true)
}

func (m *Manager) checkSupport(ctx context.Context, refresh bool) error {
	if m == nil {
		return errors.New("session isolation support is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if refresh || m.checked.IsZero() || time.Since(m.checked) >= time.Minute {
		probe, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		m.status = Status{Mode: "enabled"}
		if err := sandbox.Probe(probe, m.options); err != nil {
			m.status = Status{Mode: "unsupported", Reason: err.Error()}
		}
		m.checked = time.Now()
	}
	if m.status.Mode != "enabled" {
		return errors.New(m.status.Reason)
	}
	return nil
}
