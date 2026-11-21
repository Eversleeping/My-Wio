package childprocess

import (
	"context"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsJobCancellationKillsDescendants(t *testing.T) {
	if os.Getenv("WIO_JOB_HELPER") == "child" {
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	if os.Getenv("WIO_JOB_HELPER") == "parent" {
		cmd := exec.Command(os.Args[0], "-test.run=TestWindowsJobCancellationKillsDescendants")
		cmd.Env = append(os.Environ(), "WIO_JOB_HELPER=child")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("WIO_JOB_PID"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	file := filepath.Join(t.TempDir(), "child.pid")
	cmd := CommandContext(ctx, os.Args[0], "-test.run=TestWindowsJobCancellationKillsDescendants")
	cmd.Env = Environment(ctx, "WIO_JOB_HELPER=parent", "WIO_JOB_PID="+file)
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = Wait(cmd) }()
	deadline := time.Now().Add(10 * time.Second)
	var child int
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(file)
		child, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		if child > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("descendant did not start")
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(child))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	cancel()
	result, err := windows.WaitForSingleObject(handle, 10000)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("job left descendant running")
	}
}
