// Gemini 0.65/0.66 starts streamHistory without awaiting it. Command
// availability and even the next prompt can race that replay. Adapt only this
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
  const target = 'session.streamHistory(messages).catch(';
  if (!source.includes(target)) return result;
  if (source.indexOf(target) !== source.lastIndexOf(target)) {
    throw new Error('Ambiguous Gemini ACP history implementation');
  }
  let patched = source.replace(target,
    'await session.streamHistory(messages).then(() => { response._meta = { ...response._meta, wioHistoryReplayed: true }; }).catch(');
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
