//go:build linux

package codexadapter

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestKillProcessStopsAppServerDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 300 & echo $!; wait")
	configureProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	descendant, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	killProcess(cmd)
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("app-server was not stopped")
	}
	deadline := time.Now().Add(10 * time.Second)
	for running(descendant) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant %d survived the app-server", descendant)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// running treats an exited process awaiting reaping by init as stopped.
func running(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z"
}
