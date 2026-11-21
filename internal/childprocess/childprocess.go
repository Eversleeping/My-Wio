// Package childprocess runs short-lived helper commands whose Wait must not
// block forever.
//
// os/exec copies a command's output through pipes. If the command starts a
// descendant that inherits those pipes and outlives it (a credential cache
// daemon, a background service), Output and CombinedOutput keep waiting for
// the pipes to close even after the command exited or its context ended.
package childprocess

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"time"
)

// WaitDelay bounds how long Wait keeps reading inherited output pipes after
// the command exits or its context is done.
const WaitDelay = 5 * time.Second

// CommandContext is exec.CommandContext with a bounded Wait and a filtered
// environment. Callers can replace Env using Environment with explicit settings.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = Environment(ctx)
	cmd.WaitDelay = WaitDelay
	configurePlatformCommand(cmd)
	return cmd
}

// Result normalizes a Wait error. exec.ErrWaitDelay means the command itself
// exited successfully and only a descendant kept its output pipes open, so
// the command succeeded with everything it wrote before exiting.
func Result(err error) error {
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

// Output is cmd.Output with Result applied.
func Output(cmd *exec.Cmd) ([]byte, error) {
	if runtime.GOOS == "windows" {
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		if cmd.Stderr == nil {
			cmd.Stderr = &stderr
		}
		err := Run(cmd)
		if exit, ok := err.(*exec.ExitError); ok {
			exit.Stderr = stderr.Bytes()
		}
		return stdout.Bytes(), Result(err)
	}
	output, err := cmd.Output()
	return output, Result(err)
}

// CombinedOutput is cmd.CombinedOutput with Result applied.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if runtime.GOOS == "windows" {
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		err := Run(cmd)
		return output.Bytes(), Result(err)
	}
	output, err := cmd.CombinedOutput()
	return output, Result(err)
}
