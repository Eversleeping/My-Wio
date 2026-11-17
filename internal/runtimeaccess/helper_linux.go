package runtimeaccess

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/protocol"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const configFile = "/etc/wio-agent/config.json"
const sudoFile = "/etc/sudoers.d/wio-agent"
const overrideFile = "/etc/systemd/system/wio-agent.service.d/90-wio-access.conf"
const backupFile = "/etc/wio-agent/.runtime-access-backup.json"

type Runner func(context.Context, string, ...string) error
type Helper struct {
	Root string
	Run  Runner
}
type snapshot struct {
	Data   []byte
	Mode   uint32
	UID    int
	GID    int
	Exists bool
}
type backup struct {
	Files        map[string]snapshot
	NextRevision string
}

func (h Helper) path(name string) string { return filepath.Join(h.Root, name) }
func run(ctx context.Context, name string, args ...string) error {
	if name != "systemctl" && name != "sudo" {
		return errors.New("unsupported system helper program")
	}
	program := "/usr/bin/" + name
	if _, err := os.Stat(program); err != nil {
		program = "/bin/" + name
	}
	command := childprocess.CommandContext(ctx, program, args...)
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	var diagnostic boundedDiagnostic
	command.Stderr = &diagnostic
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(diagnostic.String()))
	}
	return nil
}

type boundedDiagnostic struct{ bytes.Buffer }

func (b *boundedDiagnostic) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - b.Len(); remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(n, remaining)])
	}
	return n, nil
}
func readSnapshot(name string) (snapshot, error) {
	s, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot{}, nil
	}
	if err != nil {
		return snapshot{}, err
	}
	if !s.Mode().IsRegular() {
		return snapshot{}, errors.New("runtime settings require regular managed files")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return snapshot{}, err
	}
	st := s.Sys().(*syscall.Stat_t)
	return snapshot{data, uint32(s.Mode().Perm()), int(st.Uid), int(st.Gid), true}, nil
}
func writeSnapshot(name string, s snapshot) error {
	if !s.Exists {
		err := os.Remove(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".runtime-access-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(s.Data); err == nil {
		err = f.Chmod(os.FileMode(s.Mode))
	}
	if err == nil {
		err = f.Chown(s.UID, s.GID)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}
func newSnapshot(data []byte, mode uint32) snapshot {
	return snapshot{data, mode, os.Geteuid(), os.Getegid(), true}
}
func decode(raw []byte, id string) (protocol.AgentAccessSettings, map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	var c struct {
		ServerID  string `json:"server_id"`
		AllowSudo bool   `json:"allow_sudo"`
	}
	if json.Unmarshal(raw, &m) != nil || m == nil || json.Unmarshal(raw, &c) != nil || id == "" || c.ServerID != id {
		return protocol.AgentAccessSettings{}, nil, errors.New("installed Agent identity or configuration is invalid")
	}
	sum := sha256.Sum256(raw)
	return protocol.AgentAccessSettings{AllowSudo: c.AllowSudo, Revision: hex.EncodeToString(sum[:])}, m, nil
}
func validRevision(s string) bool { v, e := hex.DecodeString(s); return e == nil && len(v) == 32 }
func (h Helper) read(id string) (protocol.AgentAccessSettings, map[string]json.RawMessage, error) {
	s, e := readSnapshot(h.path(configFile))
	if e != nil {
		return protocol.AgentAccessSettings{}, nil, e
	}
	return decode(s.Data, id)
}
func (h Helper) restore(ctx context.Context, b backup) error {
	var errs []error
	for name, s := range b.Files {
		if e := writeSnapshot(h.path(name), s); e != nil {
			errs = append(errs, e)
		}
	}
	if e := h.Run(ctx, "systemctl", "daemon-reload"); e != nil {
		errs = append(errs, e)
	}
	return errors.Join(errs...)
}

func (h Helper) pending(id string) (backup, error) {
	var b backup
	raw, err := os.ReadFile(h.path(backupFile))
	if err != nil {
		return b, err
	}
	if json.Unmarshal(raw, &b) != nil || !validRevision(b.NextRevision) || len(b.Files) != 3 {
		return b, errors.New("invalid pending runtime settings backup")
	}
	for _, name := range []string{configFile, sudoFile, overrideFile} {
		if _, ok := b.Files[name]; !ok {
			return b, errors.New("invalid pending runtime settings backup")
		}
	}
	if _, _, err := decode(b.Files[configFile].Data, id); err != nil {
		return b, errors.New("pending runtime settings server identity differs")
	}
	return b, nil
}

func (h Helper) restored(b backup) bool {
	for name, before := range b.Files {
		now, err := readSnapshot(h.path(name))
		if err != nil || now.Exists != before.Exists || now.Mode != before.Mode || now.UID != before.UID || now.GID != before.GID || !bytes.Equal(now.Data, before.Data) {
			return false
		}
	}
	return true
}

func (h Helper) Handle(ctx context.Context, r Request) (protocol.AgentAccessSettings, error) {
	if h.Run == nil {
		h.Run = run
	}
	current, _, err := h.read(r.ServerID)
	if err != nil {
		return current, err
	}
	if r.Force && r.Action != "restart" {
		return current, errors.New("only an explicit Agent restart may be forced")
	}
	if r.Action == "read" && r.Settings == nil && r.Revision == "" {
		pending, err := h.pending(r.ServerID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return current, err
		}
		current.RestartRequired = err == nil && pending.NextRevision == current.Revision
		return current, nil
	}
	if r.Action != "apply" && r.Action != "restart" {
		return current, errors.New("unsupported runtime settings action")
	}
	lock, err := os.OpenFile(h.path("/etc/wio-agent/.runtime-access.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return current, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return current, errors.New("another runtime settings change is in progress")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, fields, err := h.read(r.ServerID)
	if err != nil {
		return current, err
	}
	previous, backupErr := h.pending(r.ServerID)
	if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
		return current, backupErr
	}
	if r.Action == "restart" {
		if !validRevision(r.Revision) || r.Revision != current.Revision || r.Settings != nil {
			return current, errors.New("runtime settings changed before restart")
		}
		hasBackup := backupErr == nil
		if hasBackup && previous.NextRevision != current.Revision && r.Force && h.restored(previous) {
			if err := h.Run(ctx, "systemctl", "daemon-reload"); err != nil {
				return current, fmt.Errorf("could not finish restoring Agent service settings: %w", err)
			}
			if err := os.Remove(h.path(backupFile)); err != nil {
				return current, err
			}
			hasBackup = false
		}
		if !hasBackup && !r.Force {
			return current, nil
		}
		if hasBackup && previous.NextRevision != current.Revision {
			return current, errors.New("pending runtime settings revision differs")
		}
		// A service restart disconnects its client; it must not cancel rollback.
		restartCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err = h.Run(restartCtx, "systemctl", "restart", "wio-agent.service"); err != nil {
			if !hasBackup {
				return current, fmt.Errorf("Agent restart failed: %w", err)
			}
			rollbackErr := h.restore(restartCtx, previous)
			oldRestartErr := h.Run(restartCtx, "systemctl", "restart", "wio-agent.service")
			if rollbackErr == nil && oldRestartErr == nil {
				_ = os.Remove(h.path(backupFile))
			}
			return current, errors.Join(errors.New("Agent restart failed; previous settings were restored when possible"), rollbackErr, oldRestartErr)
		}
		if !hasBackup {
			return current, nil
		}
		return current, os.Remove(h.path(backupFile))
	}
	next := r.Settings
	if next == nil || !validRevision(next.Revision) || next.Revision != current.Revision {
		return current, errors.New("server settings changed; read them again before applying")
	}
	if next.SessionIsolation {
		return current, errors.New("session isolation has been removed")
	}
	if backupErr == nil {
		if h.restored(previous) {
			// A failed apply can restore every file while daemon-reload still
			// fails. Retry that reload before clearing its recovery record;
			// otherwise its staged revision permanently blocks later changes.
			if err := h.Run(ctx, "systemctl", "daemon-reload"); err != nil {
				return current, fmt.Errorf("could not finish restoring Agent service settings: %w", err)
			}
			if err := os.Remove(h.path(backupFile)); err != nil {
				return current, err
			}
		} else if previous.NextRevision == current.Revision && next.AllowSudo == current.AllowSudo {
			current.RestartRequired = true
			return current, nil
		} else {
			return current, errors.New("finish the pending Agent restart before changing settings")
		}
	}
	if next.AllowSudo == current.AllowSudo {
		return current, nil
	}
	if _, err = os.Stat(h.path("/etc/systemd/system/wio-agent.service")); err != nil {
		return current, errors.New("runtime settings require the managed Agent system service")
	}
	if next.AllowSudo {
		if err = h.Run(ctx, "sudo", "-V"); err != nil {
			return current, fmt.Errorf("sudo is unavailable on the server: %w", err)
		}
	}
	fields["allow_sudo"], _ = json.Marshal(next.AllowSudo)
	nextRaw, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return current, err
	}
	result, _, err := decode(nextRaw, r.ServerID)
	if err != nil {
		return current, err
	}
	previous = backup{Files: map[string]snapshot{}, NextRevision: result.Revision}
	for _, name := range []string{configFile, sudoFile, overrideFile} {
		s, e := readSnapshot(h.path(name))
		if e != nil {
			return current, e
		}
		previous.Files[name] = s
	}
	backupRaw, _ := json.Marshal(previous)
	if err = writeSnapshot(h.path(backupFile), newSnapshot(backupRaw, 0600)); err != nil {
		return current, err
	}
	rollback := func(cause error) (protocol.AgentAccessSettings, error) {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		e := h.restore(rollbackCtx, previous)
		if e == nil {
			_ = os.Remove(h.path(backupFile))
		}
		return current, errors.Join(cause, e)
	}
	config := previous.Files[configFile]
	config.Data = nextRaw
	if err = writeSnapshot(h.path(configFile), config); err != nil {
		return rollback(err)
	}
	policy := snapshot{}
	if next.AllowSudo {
		policy = newSnapshot([]byte("wio-agent ALL=(ALL:ALL) NOPASSWD: ALL\n"), 0440)
	}
	if err = writeSnapshot(h.path(sudoFile), policy); err != nil {
		return rollback(err)
	}
	hardened, protect, home := "true", "strict", "read-only"
	if next.AllowSudo {
		hardened, protect, home = "false", "false", "false"
	}
	unit := fmt.Sprintf("[Service]\nNoNewPrivileges=%s\nProtectSystem=%s\nProtectHome=%s\nRestrictSUIDSGID=%s\n", hardened, protect, home, hardened)
	if err = writeSnapshot(h.path(overrideFile), newSnapshot([]byte(unit), 0644)); err != nil {
		return rollback(err)
	}
	if err = h.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return rollback(errors.New("could not reload Agent service settings"))
	}
	result.RestartRequired = true
	return result, nil
}

// ServeOne is also used by the transient local Docker helper. No external image
// is pulled: its only executable is the exact running static Agent binary.
func ServeOne(in io.Reader, out io.Writer, hostRoot bool) error {
	if os.Geteuid() != 0 {
		return errors.New("runtime settings helper requires root")
	}
	var r Request
	d := json.NewDecoder(io.LimitReader(in, 65536))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one runtime settings request")
	}
	if hostRoot {
		if err := syscall.Chroot("/host"); err != nil {
			return err
		}
		if err := os.Chdir("/"); err != nil {
			return err
		}
	}
	settings, err := (Helper{Root: "/"}).Handle(context.Background(), r)
	reply := Reply{Settings: settings}
	if err != nil {
		reply.Error = err.Error()
	}
	var encoded bytes.Buffer
	if e := json.NewEncoder(&encoded).Encode(reply); e != nil {
		return e
	}
	_, e := out.Write(encoded.Bytes())
	return e
}
