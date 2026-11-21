package childprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func configurePlatformCommand(cmd *exec.Cmd) {
	// npm .cmd wrappers cannot be passed directly to CreateProcess. Launch their
	// underlying JS file with node instead of interpolating user input into cmd.exe.
	if resolved, err := exec.LookPath(cmd.Path); err == nil && (strings.HasSuffix(strings.ToLower(resolved), ".cmd") || strings.HasSuffix(strings.ToLower(resolved), ".bat")) {
		scripts := map[string]string{"codex.cmd": "@openai/codex/bin/codex.js", "gemini.cmd": "@google/gemini-cli/dist/index.js", "claude.cmd": "@anthropic-ai/claude-code/cli.js", "npm.cmd": "npm/bin/npm-cli.js", "npx.cmd": "npm/bin/npx-cli.js"}
		if script, ok := scripts[strings.ToLower(filepath.Base(resolved))]; ok {
			root := filepath.Join(filepath.Dir(resolved), "node_modules")
			if filepath.Base(filepath.Dir(resolved)) == ".bin" {
				root = filepath.Dir(filepath.Dir(resolved))
			}
			entry := filepath.Join(root, filepath.FromSlash(script))
			if node, err := exec.LookPath("node.exe"); err == nil {
				cmd.Path = node
				cmd.Args = append([]string{node, entry}, cmd.Args[1:]...)
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
