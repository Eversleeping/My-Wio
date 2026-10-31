package geminiadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryLoaderWaitsForReplayAndPreservesInstalledSource(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js not installed")
	}
	// Exercise the actual Node loader API, including a path with spaces.
	directory := filepath.Join(t.TempDir(), "CLI with spaces", "node_modules", "@google", "gemini-cli")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "gemini.mjs")
	source := []byte(`
const events = [];
const session = { async streamHistory(messages) {
  for (const message of messages) {
    await new Promise(resolve => setTimeout(resolve, 10));
    events.push(message);
  }
} };
async function loadSession() {
  const messages = ['old1', 'old2', 'old3'];
  const response = {};
  session.streamHistory(messages).catch(() => {});
  return response;
}
const response = await loadSession();
if (process.env.NODE_OPTIONS !== '--no-warnings') process.exit(3);
if (!response._meta?.wioHistoryReplayed || events.join(',') !== 'old1,old2,old3') process.exit(2);
console.log('REPLAY_FINISHED');
`)
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	environment, cleanup, err := acpHistoryEnvironment(append(os.Environ(), "NODE_OPTIONS=--no-warnings"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, path)
	cmd.Env = environment
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "REPLAY_FINISHED") {
		t.Fatalf("loader did not fence replay: %v %s", err, output)
	}
	if current, err := os.ReadFile(path); err != nil || string(current) != string(source) {
		t.Fatal("loader changed the installed source")
	}
	userPath := filepath.Join(t.TempDir(), "user-tool.mjs")
	userSource := strings.Replace(string(source), "if (!response._meta?.wioHistoryReplayed || events.join(',') !== 'old1,old2,old3') process.exit(2);", "if (response._meta !== undefined || events.length !== 0) process.exit(2);", 1)
	userSource = strings.Replace(userSource, "if (process.env.NODE_OPTIONS !== '--no-warnings') process.exit(3);", "", 1)
	if err := os.WriteFile(userPath, []byte(userSource), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, node, userPath)
	cmd.Env = environment
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("loader modified an unrelated Node tool: %v %s", err, output)
	}
	// Match the real CLI wrapper: the actual ACP module loads only in a
	// relaunched child. It still needs the hook, then must clear its own flags.
	wrapper := filepath.Join(t.TempDir(), "wrapper.mjs")
	if err := os.WriteFile(wrapper, []byte(`import { spawnSync } from 'node:child_process';
const child = spawnSync(process.execPath, [process.argv[2]], { stdio: 'inherit' });
process.exit(child.status ?? 1);`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, node, wrapper, path)
	cmd.Env = environment
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("loader did not survive CLI relaunch: %v %s", err, output)
	}
}
