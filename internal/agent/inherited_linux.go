//go:build linux

package agent

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// ReapInheritedChildren adopts processes that a previous Agent image started
// before replacing itself with syscall.Exec during a self-update. Exec keeps
// the PID, so those processes remain children of this process, but the new
// image has no exec.Cmd for them and would otherwise never wait for them;
// every exited one would stay a zombie until the service restarts.
//
// It must run before this image starts any child process of its own, because
// every child that exists at that point is treated as inherited.
func ReapInheritedChildren(log *slog.Logger) int {
	children := inheritedChildren("/proc", os.Getpid())
	for _, pid := range children {
		go func(pid int) {
			var status syscall.WaitStatus
			for {
				_, err := syscall.Wait4(pid, &status, 0, nil)
				if err != syscall.EINTR {
					return
				}
			}
		}(pid)
	}
	if len(children) > 0 && log != nil {
		log.Info("reaping processes inherited from the previous Agent image", "count", len(children), "pids", children)
	}
	return len(children)
}

func inheritedChildren(procRoot string, parent int) []int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var children []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == parent {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		if ppid, ok := parentPID(stat); ok && ppid == parent {
			children = append(children, pid)
		}
	}
	return children
}

// parentPID extracts the fourth field of /proc/<pid>/stat. The command name in
// the second field may contain spaces and parentheses, so parse after the last
// closing parenthesis.
func parentPID(stat []byte) (int, bool) {
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := bytes.Fields(stat[end+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(string(fields[1]))
	return ppid, err == nil
}
