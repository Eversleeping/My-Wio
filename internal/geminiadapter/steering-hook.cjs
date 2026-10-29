// Per-process BeforeModel hook. Model requests stay in this process; only the
// local queue is queried. Never write diagnostics or credentials to stdout.
const fs = require('node:fs');
(async () => {
  const input = JSON.parse(fs.readFileSync(0, 'utf8'));
  if (!Array.isArray(input.llm_request?.messages)) throw new Error('Unsupported hook request');
  const response = await fetch(process.env.WIO_GEMINI_STEER_URL, {
    method: 'POST', headers: { Authorization: 'Bearer ' + process.env.WIO_GEMINI_STEER_AUTH },
    signal: AbortSignal.timeout(4000),
  });
  if (!response.ok) throw new Error('Steering queue unavailable');
  const { hints } = await response.json();
  if (!hints.length) return process.stdout.write('{}');
  const messages = [...input.llm_request.messages, {
    role: 'user', content: 'The user supplied these updates while this task was running. Apply them to the ongoing task in order; later updates supersede conflicting earlier ones.\n' + JSON.stringify(hints),
  }];
  process.stdout.write(JSON.stringify({ hookSpecificOutput: { llm_request: { messages } } }));
})().catch(() => { process.stderr.write('Wio could not deliver steering instructions\n'); process.exitCode = 2; });
