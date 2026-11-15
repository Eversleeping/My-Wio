import { createServer } from "node:http";
import { execFileSync, spawn } from "node:child_process";
import { appendFileSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createInterface } from "node:readline";

// This process gets an isolated home and an allowlist environment. Build with
// the developer/CI toolchain first, before substituting the runtime environment.
export default async function startControlplane() {
  if (process.platform !== "linux") throw new Error("The real Agent smoke suite requires Linux");
  const repository = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
  const root = mkdtempSync(join(tmpdir(), "wio-browser-"));
  const output = join(repository, "outputs/playwright");
  mkdirSync(output, { recursive: true });
  const logPath = join(output, "real-controlplane.log");
  writeFileSync(logPath, "");
  // The external catalog is synthetic; every browser API still reaches Go.
  const catalog = createServer((request, response) => {
    response.setHeader("Content-Type", "application/json");
    response.end(JSON.stringify(request.url.startsWith("/gemini")
      ? { models: [{ name: "models/flash", displayName: "Flash", supportedGenerationMethods: ["generateContent"] }] }
      : { data: [{ id: "sonnet", display_name: "Sonnet", type: "model" }], has_more: false }));
  });
  await new Promise((resolve, reject) => { catalog.once("error", reject); catalog.listen(0, "127.0.0.1", resolve); });
  process.env.WIO_E2E_MODEL_URL = `http://127.0.0.1:${catalog.address().port}`;
  let child;
  let exited;
  const stop = async () => {
    try {
      if (child) {
        let timer;
        child.kill("SIGTERM");
        const result = await Promise.race([
          exited,
          new Promise(resolve => { timer = setTimeout(() => resolve(null), 20_000); })
        ]);
        clearTimeout(timer);
        if (!result) {
          process.kill(-child.pid, "SIGKILL");
          await exited;
          throw new Error(`Control plane shutdown timed out; see ${logPath}`);
        }
        if (result.code !== 0) throw new Error(`Control plane exited with ${JSON.stringify(result)}; see ${logPath}`);
      }
    } finally {
      await new Promise(resolve => catalog.close(resolve));
      rmSync(root, { recursive: true, force: true });
    }
  };
  try {
    const bin = join(root, "bin");
    mkdirSync(bin);
    execFileSync("go", ["build", "-buildvcs=false", "-o", join(bin, "controlplane"), "./cmd/controlplane"], { cwd: repository, stdio: "pipe", timeout: 180_000 });
    execFileSync("go", ["build", "-buildvcs=false", "-o", join(bin, "fake-engine"), "./cmd/controlplane/testdata/fakeengine"], { cwd: repository, stdio: "pipe", timeout: 60_000 });
    for (const name of ["codex", "claude", "gemini"]) symlinkSync("fake-engine", join(bin, name));
    const workspace = join(root, "repositories", "smoke");
    mkdirSync(workspace, { recursive: true });
    const env = { PATH: `${bin}:/usr/local/bin:/usr/bin:/bin`, LANG: "C.UTF-8" };
    for (const name of ["HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "GEMINI_CLI_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "TMPDIR"]) {
      env[name] = join(root, name.toLowerCase());
      mkdirSync(env[name], { mode: 0o700 });
    }
    Object.assign(env, {
      GEMINI_CLI_SYSTEM_SETTINGS_PATH: join(root, "gemini-system.json"),
      GEMINI_CLI_SYSTEM_DEFAULTS_PATH: join(root, "gemini-defaults.json"),
      WIO_ADDR: "127.0.0.1:0",
      WIO_DATABASE_URL: `${join(root, "wio.db")}?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)`,
      WIO_DEV_INSECURE: "true",
      WIO_SETUP_TOKEN: "p0-08-browser-setup-token",
      WIO_CONTROL_AGENT_ENABLED: "true",
      WIO_CONTROL_AGENT_SCAN_ROOTS: join(root, "repositories"),
      WIO_CONTROL_AGENT_CLONE_ROOT: join(root, "clones"),
      WIO_CONTROL_AGENT_STATE_DIR: join(root, "state"),
      WIO_CONTROL_AGENT_CODEX_PATH: join(bin, "codex"),
      WIO_CONTROL_AGENT_CODEX_KEY_FILE: join(root, "codex.key"),
      WIO_CONTROL_AGENT_DOCKER_PATH: join(root, "no-docker"),
      WIO_CONTROL_AGENT_PREREQUISITE_SOCKET: join(root, "prerequisite.sock")
    });
    execFileSync("git", ["init", "--initial-branch=main", workspace], { env, stdio: "pipe" });
    writeFileSync(join(workspace, "README.md"), "Browser smoke workspace\n");
    child = spawn(join(bin, "controlplane"), [], { cwd: root, env, detached: true, stdio: ["ignore", "pipe", "pipe"] });
    exited = new Promise((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", (code, signal) => resolve({ code, signal }));
    });
    for (const stream of [child.stdout, child.stderr]) stream.on("data", data => appendFileSync(logPath, data));
    const lines = createInterface({ input: child.stdout });
    let timer;
    try {
      const baseURL = await Promise.race([
        new Promise(resolve => {
          lines.on("line", line => {
            try {
              const entry = JSON.parse(line);
              if (entry.msg === "Wio control plane listening") resolve(`http://${entry.address}`);
            } catch { /* Non-JSON diagnostics remain in the service log. */ }
          });
        }),
        exited.then(result => { throw new Error(`Control plane stopped during startup: ${JSON.stringify(result)}; see ${logPath}`); }),
        new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`Control plane startup timed out; see ${logPath}`)), 30_000); })
      ]);
      process.env.WIO_E2E_URL = baseURL;
      process.env.WIO_E2E_WORKSPACE_PATH = workspace;
    } finally {
      clearTimeout(timer);
      lines.close();
      child.stdout.resume();
    }
    return stop;
  } catch (error) {
    try { await stop(); } catch (shutdownError) { throw new AggregateError([error, shutdownError], "Real service startup failed"); }
    throw error;
  }
}
