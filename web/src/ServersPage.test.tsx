import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ServersPage } from "./App";
import { I18nProvider } from "./i18n";

const server = {
  id: "server-1",
  name: "retire-node",
  hostname: "retire-node.local",
  status: "online",
  is_control_plane: false,
  agent_version: "0.2.9",
  agent_target_version: "0.2.9",
  agent_update_available: false,
  agent_update_supported: true,
  codex_version: "0.144.4",
  codex_ready: 1,
  codex_target_version: "0.144.4",
  codex_update_available: false,
  codex_update_supported: true,
  address: "192.0.2.10",
  configuration: "",
  notes: "",
  codex_profile_id: "",
  codex_profile_name: "",
  git_profile_id: "",
  git_profile_name: "",
  last_seen_at: "2026-07-27T10:00:00Z",
  created_at: "2026-07-01T10:00:00Z"
};

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

test("verifies the host fingerprint before completely retiring a server", async () => {
  window.localStorage.setItem("wio_language", "en");
  const notify = vi.fn();
  const requests: Array<{ url: string; method: string; body: string }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    const body = String(init.body ?? "");
    requests.push({ url, method, body });
    let payload: unknown = [];
    if (url === "/api/servers" && method === "GET") payload = [server];
    else if (url === "/api/credential-profiles" && method === "GET") payload = [];
    else if (url === "/api/servers/ssh/probe" && method === "POST") payload = { fingerprint: "SHA256:retire-host", key_type: "ssh-ed25519" };
    else if (url === `/api/servers/${server.id}/ssh/uninstall` && method === "POST") payload = { server_id: server.id, hostname: server.hostname };
    return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
  }));

  const user = userEvent.setup();
  render(<I18nProvider><ServersPage realtime={0} notify={notify} /></I18nProvider>);
  await user.click(await screen.findByRole("button", { name: "Retire server" }));

  let dialog = screen.getByRole("dialog", { name: "Retire retire-node" });
  expect(within(dialog).getByText(/Projects outside the Agent-managed directory/)).toBeInTheDocument();
  expect(within(dialog).getByText(/workspace, session, deployment, and orphaned project records/)).toBeInTheDocument();
  await user.selectOptions(within(dialog).getByRole("combobox", { name: "Authentication" }), "password");
  await user.type(within(dialog).getByRole("textbox", { name: "Type retire-node to confirm" }), server.name);
  await user.type(within(dialog).getByLabelText("SSH password"), "temporary-ssh-secret");
  await user.click(within(dialog).getByRole("button", { name: "Check fingerprint" }));

  dialog = await screen.findByRole("dialog", { name: "Retire retire-node" });
  expect(await within(dialog).findByText("SHA256:retire-host")).toBeInTheDocument();
  expect(requests).toContainEqual({
    url: "/api/servers/ssh/probe",
    method: "POST",
    body: JSON.stringify({ host: server.address, port: 22 })
  });
  await user.click(within(dialog).getByRole("button", { name: "Retire completely" }));

  await waitFor(() => expect(notify).toHaveBeenCalledWith("Server fully retired"));
  const uninstall = requests.find(request => request.url === `/api/servers/${server.id}/ssh/uninstall`);
  expect(uninstall?.method).toBe("POST");
  expect(JSON.parse(uninstall?.body ?? "{}")).toMatchObject({
    host: server.address,
    port: 22,
    user: "root",
    auth_method: "password",
    password: "temporary-ssh-secret",
    private_key: "",
    host_key_fingerprint: "SHA256:retire-host",
    confirmation: server.name
  });
  expect(requests.some(request => request.method === "DELETE")).toBe(false);
});
