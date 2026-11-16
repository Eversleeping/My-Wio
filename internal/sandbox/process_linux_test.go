//go:build linux

package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCompleteLinuxProcessBoundary(t *testing.T) {
	options := Options{Bwrap: os.Getenv("WIO_SANDBOX_TEST_BWRAP")}
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
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Fatal("single-threaded namespace positive control requires unshare")
	}
	credential := func(cmd *exec.Cmd) {
		if os.Geteuid() == 0 {
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: options.UID, Gid: options.GID, Groups: []uint32{}}}
		}
	}
	positive := exec.CommandContext(ctx, unshare, "--user", "--map-current-user", "true")
	positive.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	credential(positive)
	if output, err := positive.CombinedOutput(); err != nil {
		t.Fatalf("the same account cannot create a namespace outside the sandbox: %v\n%s", err, output)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	receiverContext, stopReceiver := context.WithCancel(ctx)
	defer stopReceiver()
	receiver := exec.CommandContext(receiverContext, helper, "-test.run=^TestLinuxSignalReceiver$")
	receiver.Env = []string{"WIO_SANDBOX_SIGNAL_RECEIVER=1"}
	credential(receiver)
	stdout, err := receiver.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stopReceiver(); _ = receiver.Wait() }()
	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-receiverContext.Done():
				return
			}
		}
	}()
	waitLine := func(want string) {
		t.Helper()
		select {
		case line := <-lines:
			if line != want {
				t.Fatalf("signal receiver returned %q instead of %q", line, want)
			}
		case <-ctx.Done():
			t.Fatal("signal receiver did not confirm the positive control")
		}
	}
	waitLine("WIO_SIGNAL_READY")
	peerPID := strconv.Itoa(receiver.Process.Pid)
	positive = exec.CommandContext(ctx, "sh", "-c", `kill -USR1 "$1"`, "signal-control", peerPID)
	positive.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	credential(positive)
	if output, err := positive.CombinedOutput(); err != nil {
		t.Fatalf("outside signal positive control failed: %v\n%s", err, output)
	}
	waitLine("WIO_SIGNAL_RECEIVED")
	workspace, err := os.MkdirTemp("", "wio-process-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	if os.Geteuid() == 0 {
		if err := os.Chown(workspace, int(options.UID), int(options.GID)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(ctx, helper, "-test.run=^TestLinuxProcessInside$", "-test.v")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + workspace, "TMPDIR=" + workspace, "WIO_SANDBOX_PROCESS_INSIDE=1", "WIO_SANDBOX_PEER_PID=" + peerPID}
	policy := Policy{Mounts: append(SystemMounts(), Mount{Source: workspace, Target: workspace, Writable: true}), WorkDir: workspace}
	release, err := Prepare(cmd, policy, options)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("complete process boundary failed: %v\n%s", err, output)
	}
	var proof map[string]bool
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "WIO_PROCESS_PROOF=") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "WIO_PROCESS_PROOF=")), &proof); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(proof) != 2 || !proof["nested_namespace_denied"] || !proof["peer_signal_denied"] {
		t.Fatal("missing positive-control-backed process boundary proof")
	}
	select {
	case <-lines:
		t.Fatal("the outside receiver received a signal from the sandbox")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(workspace, "unexpected")); err == nil {
		t.Fatal("nested namespace target ran")
	}
}

func TestLinuxSignalReceiver(t *testing.T) {
	if os.Getenv("WIO_SANDBOX_SIGNAL_RECEIVER") != "1" {
		t.Skip("synthetic signal receiver only")
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	defer signal.Stop(signals)
	fmt.Println("WIO_SIGNAL_READY")
	for range signals {
		fmt.Println("WIO_SIGNAL_RECEIVED")
	}
}

func TestLinuxProcessInside(t *testing.T) {
	if os.Getenv("WIO_SANDBOX_PROCESS_INSIDE") != "1" {
		t.Skip("isolated process probe only")
	}
	pid, err := strconv.Atoi(os.Getenv("WIO_SANDBOX_PEER_PID"))
	if err != nil || pid <= 0 {
		t.Fatal("invalid synthetic peer process")
	}
	proof := map[string]bool{"peer_signal_denied": unix.Kill(pid, syscall.SIGUSR1) != nil}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Fatal("namespace probe executable is not visible in the sandbox")
	}
	output, err := exec.Command(unshare, "--user", "--map-current-user", "sh", "-c", "touch unexpected").CombinedOutput()
	proof["nested_namespace_denied"] = err != nil && (strings.Contains(string(output), "Operation not permitted") || strings.Contains(string(output), "No space left on device"))
	data, _ := json.Marshal(proof)
	fmt.Println("WIO_PROCESS_PROOF=" + string(data))
	for name, passed := range proof {
		if !passed {
			t.Errorf("failed process boundary: %s", name)
		}
	}
}
