// Older Gemini releases start streamHistory without awaiting it; newer
// releases already await it. Fence and acknowledge either implementation only
// after replay succeeds. Adapt only this
// process's module source: installed CLI files and native history stay intact.
// The private acknowledgement makes unfamiliar CLI versions fail before a
// prompt rather than silently mixing history with new output.
let originalNodeOptions;
export function initialize(data) {
  originalNodeOptions = data?.originalNodeOptions;
}

export async function load(url, context, nextLoad) {
  const result = await nextLoad(url, context);
  // NODE_OPTIONS also reaches Node-based tools and MCP servers. Their source
  // must remain untouched even if it happens to contain the same expression.
  if (!url.startsWith('file:') || !decodeURIComponent(new URL(url).pathname).includes('/@google/gemini-cli/')) return result;
  if (result.format !== 'module' || result.source == null) return result;
  const source = typeof result.source === 'string'
    ? result.source : Buffer.from(result.source).toString('utf8');
  // ACP normally discards the subagent executor's progress callback. Forward
  // only its public tool activity; the parent tool's final ACP result carries
  // the final answer. Keep this adaptation local to this CLI process.
  const execute = /const toolResult = await invocation\.execute\(\{\s*abortSignal\s*\}\);/g;
  const observed = source.replace(execute, `const toolResult = await invocation.execute({
    abortSignal,
    updateOutput: (progress) => {
      if (!progress || progress.isSubagentProgress !== true) return;
      const recentActivity = (progress.recentActivity ?? []).filter(item => item.type === 'tool_call');
      void this.sendUpdate({ sessionUpdate: 'tool_call_update', toolCallId: callId,
        _meta: { wioSubagent: { name: progress.agentName, prompt: args,
          state: progress.state, recentActivity, result: progress.result } }
      }).catch(() => {});
    }
  });`);
  const targets = [
    ['session.streamHistory(messages).catch(',
      'await session.streamHistory(messages).then(() => { response._meta = { ...response._meta, wioHistoryReplayed: true }; }).catch('],
    ['await session.streamHistory(messages);',
      'await session.streamHistory(messages); response._meta = { ...response._meta, wioHistoryReplayed: true };'],
  ].filter(([target]) => observed.includes(target));
  if (targets.length === 0) return observed === source ? result : { ...result, source: observed };
  if (targets.length !== 1) {
    throw new Error('Ambiguous Gemini ACP history implementation');
  }
  const [target, replacement] = targets[0];
  if (observed.indexOf(target) !== observed.lastIndexOf(target)) {
    throw new Error('Ambiguous Gemini ACP history implementation');
  }
  let patched = observed.replace(target, replacement);
  // The CLI entry point relaunches Node before importing the ACP module.
  // Keep our bootstrap through that relaunch, then restore the original flags
  // in the actual CLI process before it starts tools or MCP children.
  if (typeof originalNodeOptions === 'string') {
    const restore = `process.env.NODE_OPTIONS = ${JSON.stringify(originalNodeOptions)};\n`;
    const shebangEnd = patched.startsWith('#!') ? patched.indexOf('\n') + 1 : 0;
    patched = patched.slice(0, shebangEnd) + restore + patched.slice(shebangEnd);
  }
  return { ...result, source: patched };
}
