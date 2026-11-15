
export function message(error: unknown) { return error instanceof Error ? error.message : "Request failed"; }

export function pretty(value: unknown) { try { return JSON.stringify(value, null, 2); } catch { return String(value); } }

export async function copyText(value: string) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) throw new Error("Could not copy message");
}
