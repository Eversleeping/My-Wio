package childprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func configurePlatformCommand(cmd *exec.Cmd) {
	// Resolve npm's declared bin instead of assuming every CLI uses a fixed JS
	// path. Recent Claude packages declare a native executable.
	if resolved, err := exec.LookPath(cmd.Path); err == nil {
		entry, managed, err := npmEntry(resolved)
		if managed {
			if err == nil {
				switch strings.ToLower(filepath.Ext(entry)) {
				case ".exe":
					cmd.Path = entry
					cmd.Args = append([]string{entry}, cmd.Args[1:]...)
				case ".js", ".cjs", ".mjs":
					var node string
					node, err = exec.LookPath("node.exe")
					if err == nil {
						cmd.Path = node
						cmd.Args = append([]string{node, entry}, cmd.Args[1:]...)
					}
				default:
					err = fmt.Errorf("unsupported npm entry point extension: %s", filepath.Ext(entry))
				}
			}
			if err != nil && cmd.Err == nil {
				cmd.Err = err
			}
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error { return KillTree(cmd) }
}

// taskkill /T terminates descendants including node and its native CLI. It is
// invoked directly with a numeric PID, never through a command shell.
func KillTree(cmd *exec.Cmd) error {
	if closeJob(cmd) {
		return nil
	}
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	killer := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe"), "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	killer.Env = Environment(ctx)
	killer.WaitDelay = WaitDelay
	if err := killer.Run(); err != nil {
		if e := cmd.Process.Kill(); e != nil && !errors.Is(e, os.ErrProcessDone) {
			return e
		}
	}
	return nil
}
func Configure(cmd *exec.Cmd) { configurePlatformCommand(cmd) }
