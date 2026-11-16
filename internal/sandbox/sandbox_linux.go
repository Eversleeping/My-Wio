//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const minimumABI = 9

// ABI 9 adds pathname UNIX socket resolution. No path receives this right:
// sockets created in the inherited domain remain usable, while aliases of
// sockets belonging to the trusted parent are denied.
const resolveUnix = uint64(1 << 16)

func ABI() (int, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, fmt.Errorf("Landlock is unavailable: %w", errno)
	}
	if version < minimumABI {
		return int(version), errors.New("Landlock ABI 9 or later is required for pathname socket isolation and IPC/signal scopes")
	}
	return int(version), nil
}

// Prepare wraps an already configured command. The parent closes the returned
// resources after Wait, including on Start failure. Only explicitly reviewed
// ExtraFiles survive, as read-only copies inside this one namespace.
func Prepare(cmd *exec.Cmd, policy Policy, options Options) (func(), error) {
	if _, err := ABI(); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if len(cmd.Args) == 0 || cmd.Path == "" || cmd.Err != nil {
		return nil, errors.New("session CLI executable is unavailable")
	}
	helper := options.Helper
	if helper == "" {
		var err error
		helper, err = os.Executable()
		if err != nil {
			return nil, errors.New("session sandbox helper is unavailable")
		}
	}
	helper, err := filepath.EvalSymlinks(helper)
	if err != nil || !safePath(helper) {
		return nil, errors.New("session sandbox helper is unavailable")
	}
	bwrap := options.Bwrap
	if bwrap == "" {
		bwrap = "bwrap"
	}
	bwrap, err = exec.LookPath(bwrap)
	if err != nil {
		return nil, errors.New("bubblewrap is required for session isolation")
	}
	if bwrap, err = filepath.EvalSymlinks(bwrap); err != nil || !safePath(bwrap) {
		return nil, errors.New("bubblewrap executable is unavailable")
	}
	if os.Geteuid() == 0 {
		if options.UID == 0 || options.GID == 0 {
			return nil, errors.New("root session isolation requires a nonzero isolation UID and GID")
		}
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: options.UID, Gid: options.GID, Groups: []uint32{}}
		cmd.SysProcAttr.AmbientCaps = nil
	}
	if len(cmd.ExtraFiles) != len(policy.Descriptors) {
		return nil, errors.New("unreviewed inherited session descriptors")
	}
	for i, fd := range policy.Descriptors {
		if fd != 3+i || cmd.ExtraFiles[i] == nil {
			return nil, errors.New("invalid inherited session descriptor")
		}
	}
	if len(policy.Mounts) == 128 {
		return nil, errors.New("too many session sandbox mounts")
	}
	policy.Mounts = append(append([]Mount(nil), policy.Mounts...), Mount{Source: helper, Target: helperPath})
	var sources []*os.File
	closeSources := func() {
		for _, source := range sources {
			_ = source.Close()
		}
	}
	for i := range policy.Mounts {
		mount := &policy.Mounts[i]
		// Paths have already been resolved by the trusted scope builder. A
		// symlink introduced during launch must fail rather than be followed.
		fd, openErr := unix.Openat2(unix.AT_FDCWD, mount.Source, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
		if openErr != nil {
			closeSources()
			return nil, errors.New("cannot pin a required session sandbox mount")
		}
		file := os.NewFile(uintptr(fd), "session-mount")
		info, statErr := file.Stat()
		if statErr != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			file.Close()
			closeSources()
			return nil, errors.New("session sandbox mounts must be regular files or directories")
		}
		mount.Descriptor = 3 + len(cmd.ExtraFiles) + len(sources)
		sources = append(sources, file)
	}
	data, err := json.Marshal(policy)
	if err != nil || len(data) > 64<<10 {
		closeSources()
		return nil, errors.New("session sandbox rules are too large")
	}
	file, err := descriptorFile(data)
	if err != nil {
		closeSources()
		return nil, errors.New("cannot pass session sandbox rules")
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, sources...)
	cmd.ExtraFiles = append(cmd.ExtraFiles, file)
	fd := strconv.Itoa(2 + len(cmd.ExtraFiles))
	args := append([]string(nil), cmd.Args...)
	args[0], err = filepath.Abs(cmd.Path)
	if err != nil {
		file.Close()
		closeSources()
		return nil, errors.New("cannot resolve session CLI executable")
	}
	if resolved, err := filepath.EvalSymlinks(args[0]); err == nil && resolved == helper {
		args[0] = helperPath
	}
	cmd.Path = helper
	cmd.Args = append([]string{helper, Command, "launch", fd, bwrap, "--"}, args...)
	return func() { _ = file.Close(); closeSources() }, nil
}

// DescriptorFile creates a private, anonymous descriptor. It never writes a
// credential to disk; callers must only put temporary session credentials here.
func DescriptorFile(data []byte) (*os.File, error) { return descriptorFile(data) }

func descriptorFile(data []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("wio-session", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "wio-session")
	if _, err = file.Write(data); err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func readPolicy(source io.Reader) (Policy, error) {
	data, err := io.ReadAll(io.LimitReader(source, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return Policy{}, errors.New("invalid session sandbox rules")
	}
	var policy Policy
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&policy) != nil || decoder.Decode(new(any)) != io.EOF {
		return Policy{}, errors.New("invalid session sandbox rules")
	}
	return policy, policy.Validate()
}

// Run is called before normal Agent/control-plane initialization. Restrictions
// are applied on the locked thread which immediately execs the target process.
func Run(args []string) error {
	runtime.LockOSThread()
	if len(args) == 1 && args[0] == "verify" {
		if err := verifyProcess(); err != nil {
			return err
		}
		fmt.Println("WIO_SESSION_ISOLATION_READY")
		return nil
	}
	if len(args) < 4 {
		return errors.New("invalid session sandbox invocation")
	}
	switch args[0] {
	case "launch":
		if len(args) < 5 || args[3] != "--" || !safePath(args[2]) {
			return errors.New("invalid session sandbox launcher")
		}
		fd, err := strconv.Atoi(args[1])
		if err != nil || fd < 3 || fd > 256 {
			return errors.New("invalid session sandbox rule descriptor")
		}
		file := os.NewFile(uintptr(fd), "sandbox-rules")
		policy, err := readPolicy(file)
		if err != nil {
			return err
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return errors.New("cannot rewind session sandbox rules")
		}
		bwrap := []string{args[2], "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup", "--disable-userns", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", "/run"}
		for _, mount := range policy.Mounts {
			if mount.Descriptor == 0 {
				return errors.New("session mount is not pinned")
			}
			flag := "--ro-bind-fd"
			if mount.Writable {
				flag = "--bind-fd"
			}
			bwrap = append(bwrap, flag, strconv.Itoa(mount.Descriptor), mount.Target)
		}
		for _, descriptor := range policy.Descriptors {
			name := strconv.Itoa(descriptor)
			bwrap = append(bwrap, "--perms", "0600", "--file", name, "/run/wio-session/fds/"+name)
		}
		bwrap = append(bwrap, "--perms", "0400", "--file", args[1], policyPath, "--chdir", policy.WorkDir, "--", helperPath, Command, "enter", policyPath, "--")
		bwrap = append(bwrap, args[4:]...)
		return syscall.Exec(args[2], bwrap, os.Environ())
	case "enter":
		if args[1] != policyPath || args[2] != "--" {
			return errors.New("invalid session sandbox entry")
		}
		file, err := os.Open(policyPath)
		if err != nil {
			return errors.New("session sandbox rules are unavailable")
		}
		policy, err := readPolicy(file)
		file.Close()
		if err != nil {
			return err
		}
		for _, fd := range policy.Descriptors {
			original, err := unix.Open("/run/wio-session/fds/"+strconv.Itoa(fd), unix.O_RDONLY, 0)
			if err != nil {
				return errors.New("session descriptor is unavailable")
			}
			if original != fd {
				if err = unix.Dup2(original, fd); err != nil {
					unix.Close(original)
					return fmt.Errorf("cannot restore session descriptor: %w", err)
				}
				unix.Close(original)
			}
			if _, _, errno := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), unix.F_SETFD, 0); errno != 0 {
				return errors.New("cannot preserve session descriptor")
			}
		}
		if err = enforce(policy); err != nil {
			return err
		}
		if err = verifyProcess(); err != nil {
			return err
		}
		return syscall.Exec(args[3], args[3:], os.Environ())
	default:
		return errors.New("unknown session sandbox invocation")
	}
}

func enforce(policy Policy) error {
	_, err := ABI()
	if err != nil {
		return err
	}
	// Handle filesystem rights through pathname socket resolution, including
	// device IOCTL. Never grant outside-domain UNIX socket resolution.
	handled := uint64((1 << 17) - 1)
	rules := [3]uint64{handled, 0, 3}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&rules)), unsafe.Sizeof(rules), 0)
	if errno != 0 {
		return fmt.Errorf("cannot create complete session Landlock rules: %w", errno)
	}
	defer unix.Close(int(fd))
	read := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR)
	write := handled &^ uint64(unix.LANDLOCK_ACCESS_FS_MAKE_CHAR|unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK|(1<<15)) &^ resolveUnix
	mounts := append(append([]Mount(nil), policy.Mounts...), Mount{Source: "/proc", Target: "/proc"}, Mount{Source: "/dev", Target: "/dev", Writable: true}, Mount{Source: "/tmp", Target: "/tmp", Writable: true}, Mount{Source: "/run/wio-session", Target: "/run/wio-session"})
	for _, mount := range mounts {
		info, err := os.Stat(mount.Target)
		if err != nil {
			return errors.New("required session sandbox mount is unavailable")
		}
		rights := read
		if mount.Writable {
			rights = write
		}
		if !info.IsDir() {
			rights &= uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE)
		}
		parent, err := unix.Open(mount.Target, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return errors.New("cannot open required session sandbox mount")
		}
		rule := unix.LandlockPathBeneathAttr{Allowed_access: rights, Parent_fd: int32(parent)}
		_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, fd, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
		unix.Close(parent)
		if errno != 0 {
			return fmt.Errorf("cannot apply session file access rules: %w", errno)
		}
	}
	if err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return errors.New("cannot prevent session privilege escalation")
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0)
	if errno != 0 {
		return fmt.Errorf("cannot apply session IPC and signal scopes: %w", errno)
	}
	return nil
}

func verifyProcess() error {
	if os.Geteuid() == 0 {
		return errors.New("session sandbox did not drop root privileges")
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if unix.Capget(&header, &data[0]) != nil || data[0].Effective != 0 || data[1].Effective != 0 || data[0].Permitted != 0 || data[1].Permitted != 0 {
		return errors.New("session sandbox retained capabilities")
	}
	value, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0, 0)
	if errno != 0 || value != 1 {
		return errors.New("session sandbox did not prevent privilege escalation")
	}
	return nil
}

// SystemMounts includes runtime libraries and selected public system settings.
// It deliberately never binds all of /etc, /run, HOME or Agent state.
func SystemMounts() []Mount {
	var mounts []Mount
	for _, path := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/ssl", "/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/passwd", "/etc/group", "/etc/localtime", "/etc/ld.so.cache"} {
		if _, err := os.Stat(path); err == nil {
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				mounts = append(mounts, Mount{Source: resolved, Target: path})
			}
		}
	}
	return mounts
}

// Probe tests the actual wrapper and namespace policy with only synthetic
// temporary data. Its output cannot contain service environment or credentials.
func Probe(ctx context.Context, options Options) error {
	if _, err := ABI(); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "wio-isolation-probe-")
	if err != nil {
		return errors.New("cannot prepare session isolation probe")
	}
	defer os.RemoveAll(root)
	if os.Geteuid() == 0 && options.UID != 0 && options.GID != 0 {
		if err = os.Chown(root, int(options.UID), int(options.GID)); err != nil {
			return errors.New("cannot prepare unprivileged session isolation probe")
		}
	}
	helper := options.Helper
	if helper == "" {
		helper, err = os.Executable()
		if err != nil {
			return errors.New("session isolation helper is unavailable")
		}
	}
	cmd := exec.CommandContext(ctx, helper, Command, "verify")
	cmd.Dir, cmd.Env = root, []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C", "HOME=" + root, "TMPDIR=" + root}
	policy := Policy{Mounts: append(SystemMounts(), Mount{Source: root, Target: root, Writable: true}), WorkDir: root}
	release, err := Prepare(cmd, policy, options)
	if err != nil {
		return err
	}
	defer release()
	output, err := cmd.Output()
	if err != nil || !strings.Contains(string(output), "WIO_SESSION_ISOLATION_READY") {
		return errors.New("complete bubblewrap namespaces and Landlock rules are unavailable; check the kernel, namespace policy and unprivileged account")
	}
	return nil
}
