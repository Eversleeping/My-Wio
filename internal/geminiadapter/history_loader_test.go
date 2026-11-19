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
	// Modern official releases already await replay but still need the
	// private acknowledgement. Verify completed replay and source preservation.
	awaitedPath := filepath.Join(directory, "awaited-gemini.mjs")
	awaitedSource := strings.Replace(string(source), "session.streamHistory(messages).catch(() => {});", "await session.streamHistory(messages);", 1)
	if err := os.WriteFile(awaitedPath, []byte(awaitedSource), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, node, awaitedPath)
	cmd.Env = environment
	if output, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(output), "REPLAY_FINISHED") {
		t.Fatalf("loader did not acknowledge already awaited replay: %v %s", err, output)
	}
	if current, err := os.ReadFile(awaitedPath); err != nil || string(current) != awaitedSource {
		t.Fatal("loader changed the installed awaited implementation")
	}
	// Two replay sites cannot be identified as the session/load fence. Refuse
	// that implementation before executing it, preserving the installed file.
	ambiguousPath := filepath.Join(directory, "ambiguous-gemini.mjs")
	ambiguousSource := strings.Replace(awaitedSource, "await session.streamHistory(messages);", "await session.streamHistory(messages); await session.streamHistory(messages);", 1)
	if err := os.WriteFile(ambiguousPath, []byte(ambiguousSource), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, node, ambiguousPath)
	cmd.Env = environment
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "Ambiguous Gemini ACP history implementation") {
		t.Fatalf("loader did not refuse ambiguous replay: %v %s", err, output)
	}
	if current, err := os.ReadFile(ambiguousPath); err != nil || string(current) != ambiguousSource {
		t.Fatal("loader changed the installed ambiguous implementation")
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

func TestLoaderForwardsSubagentToolsThroughActualNodeModule(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js not installed")
	}
	dir := filepath.Join(t.TempDir(), "node_modules", "@google", "gemini-cli")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "subagents.mjs")
	source := `
const updates=[];
class Session {
  async sendUpdate(update) { updates.push(update); }
  async runTool() {
    const abortSignal=new AbortController().signal;
    const callId='child', args={task:'Review'};
    const invocation={ async execute({updateOutput}) {
      updateOutput({isSubagentProgress:true,agentName:'reviewer',state:'running',recentActivity:[{id:'private',type:'thought',content:'hidden'},{id:'tool',type:'tool_call',content:'Read'}]});
      return {};
    }};
    const toolResult = await invocation.execute({ abortSignal });
  }
}
await new Session().runTool();
await new Promise(resolve=>setTimeout(resolve,5));
const agent=updates[0]?._meta?.wioSubagent;
if (!agent || agent.name!=='reviewer' || agent.recentActivity.length!==1 || agent.recentActivity[0].type!=='tool_call') process.exit(2);
console.log('SUBAGENT_FORWARDED');`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	environment, cleanup, err := acpHistoryEnvironment(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, path)
	cmd.Env = environment
	if output, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(output), "SUBAGENT_FORWARDED") {
		t.Fatalf("subagent progress lost: %v %s", err, output)
	}
	if current, err := os.ReadFile(path); err != nil || string(current) != source {
		t.Fatal("installed source changed")
	}
}
