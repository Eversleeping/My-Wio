package agent

import (
	"golang.org/x/sys/unix"
	"log/slog"
	"os"
	"syscall"
)

// Exec preserves children on macOS too. Enumerate before starting new commands
// so only children inherited from the old image are reaped here.
func ReapInheritedChildren(log *slog.Logger) int {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		if log != nil {
			log.Warn("could not inspect inherited children", "error", err)
		}
		return 0
	}
	count := 0
	for _, process := range processes {
		if int(process.Eproc.Ppid) != os.Getpid() {
			continue
		}
		pid := int(process.Proc.P_pid)
		count++
		go func() {
			var status syscall.WaitStatus
			for {
				_, err := syscall.Wait4(pid, &status, 0, nil)
				if err != syscall.EINTR {
					return
				}
			}
		}()
	}
	return count
}
