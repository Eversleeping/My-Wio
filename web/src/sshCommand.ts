export type SSHConnection = { host: string; port: number; user?: string };

// Read connection fields only. Never execute the pasted command or silently
// discard options that could change which server SSH connects to.
export function parseSSHCommand(command: string): SSHConnection | null {
  const tokens: string[] = [];
  const input = command.trim();
  const token = /(?:[^\s"'\\]|"[^"]*"|'[^']*')+/y;
  let index = 0;
  while (index < input.length) {
    token.lastIndex = index;
    const match = token.exec(input);
    if (!match) return null;
    tokens.push(match[0].replace(/"([^"]*)"|'([^']*)'/g, (_, double: string | undefined, single: string | undefined) => double ?? single ?? ""));
    index = token.lastIndex;
    if (index < input.length && !/\s/.test(input[index])) return null;
    while (index < input.length && /\s/.test(input[index])) index++;
  }
  if (!["ssh", "ssh.exe"].includes(tokens.shift() ?? "")) return null;
  let destination = "", port: number | undefined, user: string | undefined;
  for (let i = 0; i < tokens.length; i++) {
    const value = tokens[i];
    if (value.startsWith("-p")) {
      const raw = value.slice(2) || tokens[++i];
      if (port !== undefined || !/^\d+$/.test(raw ?? "")) return null;
      port = Number(raw);
      if (port < 1 || port > 65535) return null;
    } else if (value.startsWith("-l")) {
      if (user !== undefined) return null;
      user = value.slice(2) || tokens[++i];
      if (!user) return null;
    } else if (value.startsWith("-") || destination) return null;
    else destination = value;
  }
  const parts = destination.split("@");
  if (parts.length > 2 || !parts.at(-1)) return null;
  if (parts.length === 2) {
    if (user !== undefined && user !== parts[0]) return null;
    user = parts[0];
  }
  if (user !== undefined && (!/^[a-z\d_.+-]+$/i.test(user) || user.length > 64)) return null;
  let host = parts.at(-1)!;
  if (host.startsWith("[") && host.endsWith("]")) {
    host = host.slice(1, -1);
    if (!host.includes(":")) return null;
  }
  if (host.includes(":")) {
    try { new URL(`http://[${host}]`); } catch { return null; }
  } else if (host.length > 253 || !host.split(".").every(part => /^[a-z\d](?:[a-z\d-]{0,61}[a-z\d])?$/i.test(part))) return null;
  return { host, port: port ?? 22, ...(user !== undefined ? { user } : {}) };
}
