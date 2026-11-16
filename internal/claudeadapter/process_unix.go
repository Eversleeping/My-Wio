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
	return pipeDescriptor(cmd, []byte(key))
}

// mcpConfigArgument passes private HTTP headers through the CLI's supported
// JSON file interface without placing their values on the command line.
func mcpConfigArgument(cmd *exec.Cmd, configuration []byte) (string, func(), error) {
	descriptor, release, err := pipeDescriptor(cmd, configuration)
	if err != nil {
		return "", nil, err
	}
	return "/dev/fd/" + descriptor, release, nil
}

func pipeDescriptor(cmd *exec.Cmd, content []byte) (string, func(), error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", nil, err
	}
	// Wio's key and generated MCP endpoint configuration fit in a pipe buffer.
	if _, err := writer.Write(content); err != nil {
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
