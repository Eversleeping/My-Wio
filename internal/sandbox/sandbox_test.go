package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Command {
		if err := Run(os.Args[2:]); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMalformedPolicyCannotReachAnExecutable(t *testing.T) {
	base := Policy{Mounts: []Mount{{Source: "/usr", Target: "/usr"}}, WorkDir: "/tmp/workspace"}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.WorkDir = "/" },
		func(p *Policy) { p.Mounts[0].Source = "/tmp/../etc" },
		func(p *Policy) { p.Mounts[0].Target = "/" },
		func(p *Policy) { p.Descriptors = []int{3, 3} },
		func(p *Policy) { p.Descriptors = []int{0} },
	} {
		policy := base
		policy.Mounts = append([]Mount(nil), base.Mounts...)
		change(&policy)
		if policy.Validate() == nil {
			t.Fatal("unsafe policy was accepted")
		}
	}
}

func TestUnavailableBoundaryNeverStartsTheTarget(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "started")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "touch \"$1\"", "sh", marker)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	policy := Policy{Mounts: []Mount{{Source: root, Target: root, Writable: true}}, WorkDir: root}
	_, err := Prepare(cmd, policy, Options{Bwrap: filepath.Join(root, "missing-bwrap")})
	if err == nil {
		t.Fatal("missing complete boundary was accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("failed preparation ran its target")
	}
	if cmd.Process != nil {
		t.Fatal("failed preparation started a process")
	}
}

func TestCompleteSandboxBoundary(t *testing.T) {
	if _, err := ABI(); err != nil {
		if os.Getenv("WIO_SANDBOX_TEST_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skipf("complete session isolation unavailable: %v", err)
	}
	if runtime.GOOS != "linux" {
		t.Skip("Linux session isolation required")
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Helper: helper, Bwrap: os.Getenv("WIO_SANDBOX_TEST_BWRAP")}
	if os.Geteuid() == 0 {
		options.UID, options.GID = 1000, 1000
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Probe(ctx, options); err != nil {
		if os.Getenv("WIO_SANDBOX_TEST_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skipf("complete session isolation unavailable: %v", err)
	}
	root, err := os.MkdirTemp("", "wio-sandbox-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	workspace, private, state, temp := filepath.Join(root, "workspace"), filepath.Join(root, "private"), filepath.Join(root, "engine-state"), filepath.Join(root, "session-temp")
	for _, dir := range []string{workspace, private, state, temp, filepath.Join(root, "other-workspace")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(private, "credential")
	if err := os.WriteFile(outside, []byte("synthetic-upstream-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other-workspace", "source"), []byte("synthetic other project"), 0600); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(private, "readonly")
	if err := os.WriteFile(readonly, []byte("public-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if options.UID != 0 {
		if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Chown(path, int(options.UID), int(options.GID))
		}); err != nil {
			t.Fatal(err)
		}
	}
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	addresses := []string{filepath.Join(private, "docker.sock"), filepath.Join(private, "installer.sock"), "@wio-sandbox-" + filepath.Base(root)}
	for _, address := range addresses {
		listener, err := net.Listen("unix", address)
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		if !strings.HasPrefix(address, "@") {
			os.Chmod(address, 0777)
		}
	}
	// A socket inode can already have a name inside the workspace before the
	// session starts. Hiding its original path alone does not isolate it.
	for _, name := range []string{"docker.sock", "installer.sock"} {
		alias := filepath.Join(workspace, name)
		if err := os.Link(filepath.Join(private, name), alias); err != nil {
			t.Fatal(err)
		}
		connection, err := net.DialTimeout("unix", alias, time.Second)
		if err != nil {
			t.Fatalf("outside socket alias positive control failed: %v", err)
		}
		connection.Close()
	}
	loopback, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer loopback.Close()
	cmd := exec.CommandContext(ctx, helper, "-test.run=^TestSandboxInsideProbe$", "-test.v")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + state, "TMPDIR=" + temp, "WIO_SANDBOX_INSIDE=1", "WIO_SANDBOX_ROOT=" + root, "WIO_SANDBOX_ABSTRACT=" + addresses[2], "WIO_SANDBOX_LOOPBACK=" + loopback.Addr().String()}
	lease, err := DescriptorFile([]byte("synthetic-temporary-session-lease"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	cmd.ExtraFiles = []*os.File{lease}
	policy := Policy{WorkDir: workspace, Descriptors: []int{3}, Mounts: append(SystemMounts(), Mount{Source: workspace, Target: workspace, Writable: true}, Mount{Source: state, Target: state, Writable: true}, Mount{Source: temp, Target: temp, Writable: true}, Mount{Source: readonly, Target: "/runtime/sentinel"})}
	release, err := Prepare(cmd, policy, options)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("complete sandbox probe failed: %v\n%s", err, output)
	}
	var proof map[string]bool
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "WIO_BOUNDARY_PROOF=") {
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "WIO_BOUNDARY_PROOF=")), &proof) != nil {
				t.Fatal("invalid complete boundary proof")
			}
		}
	}
	if len(proof) < 10 {
		t.Fatal("missing complete boundary proof")
	}
	for name, passed := range proof {
		if !passed {
			t.Errorf("boundary check failed: %s", name)
		}
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "synthetic-upstream-marker" {
		t.Fatal("outside credential changed")
	}
	info, err := os.Stat(readonly)
	if err != nil || info.Mode().Perm() != 0600 || info.ModTime().Unix() == 1 {
		t.Fatal("outside metadata changed")
	}
}

func TestMountPathReplacementCannotExpandTheSandbox(t *testing.T) {
	if os.Getenv("WIO_SANDBOX_TEST_REQUIRED") != "1" {
		t.Skip("real supported kernel required; run with WIO_SANDBOX_TEST_REQUIRED=1")
	}
	root, err := os.MkdirTemp("", "wio-pinned-mount-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	public, private := filepath.Join(root, "public"), filepath.Join(root, "private")
	if err := os.WriteFile(public, []byte("public-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(private, []byte("synthetic-upstream-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{Bwrap: os.Getenv("WIO_SANDBOX_TEST_BWRAP")}
	if os.Geteuid() == 0 {
		options.UID, options.GID = 1000, 1000
		for _, path := range []string{root, workspace, public, private} {
			if err := os.Chown(path, 1000, 1000); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "cat /runtime/sentinel")
	cmd.Env, cmd.Dir = []string{"PATH=/usr/bin:/bin"}, workspace
	policy := Policy{Mounts: append(SystemMounts(), Mount{Source: workspace, Target: workspace, Writable: true}, Mount{Source: public, Target: "/runtime/sentinel"}), WorkDir: workspace}
	release, err := Prepare(cmd, policy, options)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.Rename(public, public+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, public); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(workspace, workspace+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, workspace); err != nil {
		t.Fatal(err)
	}
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "public-sentinel" {
		t.Fatalf("launch followed a replaced mount path: %v\n%s", err, output)
	}
}

func TestSandboxInsideProbe(t *testing.T) {
	if os.Getenv("WIO_SANDBOX_INSIDE") != "1" {
		t.Skip("isolated child only")
	}
	root := os.Getenv("WIO_SANDBOX_ROOT")
	proof := map[string]bool{}
	workspace := filepath.Join(root, "workspace")
	created := filepath.Join(workspace, "created")
	proof["workspace_write"] = os.WriteFile(created, []byte("synthetic result"), 0600) == nil
	proof["workspace_chmod"] = os.Chmod(created, 0700) == nil
	proof["workspace_timestamps"] = os.Chtimes(created, time.Unix(1, 0), time.Unix(1, 0)) == nil
	_, err := os.Open(filepath.Join(root, "private", "credential"))
	proof["credential_hidden"] = err != nil
	_, err = os.Open(filepath.Join(root, "other-workspace", "source"))
	proof["other_workspace_hidden"] = err != nil
	proof["outside_write_denied"] = os.WriteFile(filepath.Join(root, "private", "changed"), []byte("changed"), 0600) != nil
	proof["readonly_chmod_denied"] = os.Chmod("/runtime/sentinel", 0777) != nil
	proof["readonly_timestamps_denied"] = os.Chtimes("/runtime/sentinel", time.Unix(1, 0), time.Unix(1, 0)) != nil
	for name, address := range map[string]string{"docker_socket_denied": filepath.Join(root, "private", "docker.sock"), "installer_socket_denied": filepath.Join(root, "private", "installer.sock"), "abstract_socket_denied": os.Getenv("WIO_SANDBOX_ABSTRACT"), "docker_alias_denied": filepath.Join(workspace, "docker.sock"), "installer_alias_denied": filepath.Join(workspace, "installer.sock")} {
		connection, err := net.DialTimeout("unix", address, 300*time.Millisecond)
		proof[name] = err != nil
		if err == nil {
			connection.Close()
		}
	}
	internal, listenErr := net.Listen("unix", filepath.Join(workspace, "internal.sock"))
	proof["internal_socket_available"] = listenErr == nil
	if listenErr == nil {
		connection, err := net.DialTimeout("unix", internal.Addr().String(), time.Second)
		proof["internal_socket_available"] = err == nil
		if err == nil {
			connection.Close()
		}
		internal.Close()
	}
	connection, err := net.DialTimeout("tcp", os.Getenv("WIO_SANDBOX_LOOPBACK"), time.Second)
	proof["loopback_available"] = err == nil
	if err == nil {
		connection.Close()
	}
	proof["privileges_dropped"] = verifyChildPrivileges() == nil
	lease := os.NewFile(3, "session-lease")
	leaseData, leaseErr := io.ReadAll(lease)
	lease.Close()
	proof["reviewed_descriptor_restored"] = leaseErr == nil && string(leaseData) == "synthetic-temporary-session-lease"
	proof["parent_environment_hidden"] = true
	entries, _ := filepath.Glob("/proc/[0-9]*/environ")
	for _, entry := range entries {
		data, err := os.ReadFile(entry)
		if err == nil && strings.Contains(string(data), "wio-sandbox-parent-only-value") {
			proof["parent_environment_hidden"] = false
		}
	}
	data, _ := json.Marshal(proof)
	io.WriteString(os.Stdout, "WIO_BOUNDARY_PROOF="+string(data)+"\n")
	for name, passed := range proof {
		if !passed {
			t.Errorf("failed boundary: %s", name)
		}
	}
}
