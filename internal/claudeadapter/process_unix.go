//go:build !windows

package claudeadapter

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}

// apiKeyDescriptor hands key to the CLI through an inherited pipe and returns
// the descriptor number the CLI reads it from, plus a function that closes the
// parent's copy of the read end.
func apiKeyDescriptor(cmd *exec.Cmd, key string) (string, func(), error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", nil, err
	}
	// The key is far smaller than a pipe buffer, so this cannot block.
	if _, err := writer.WriteString(key); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return "", nil, err
	}
	if err := writer.Close(); err != nil {
		_ = reader.Close()
		return "", nil, err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, reader)
	// ExtraFiles[i] becomes descriptor 3+i in the child.
	return strconv.Itoa(2 + len(cmd.ExtraFiles)), func() { _ = reader.Close() }, nil
}
