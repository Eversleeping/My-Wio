import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { ServerAccessSettings } from "./ServerAccessSettings";

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

test("reads real settings before applying and clears them when selecting another server", async () => {
 localStorage.setItem("wio_language", "en");
 const requests: Record<string, unknown>[] = [];
 const revision = "a".repeat(64);
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, options: RequestInit = {}) => {
  const path = String(input);
  if (path.endsWith("/servers")) return new Response(JSON.stringify([{id:"node",name:"Node",address:"node.test"},{id:"control",name:"Control",is_control_plane:true}]));
  if (path.endsWith("/probe")) return new Response(JSON.stringify({fingerprint:"SHA256:test",key_type:"ssh-ed25519"}));
  const body = JSON.parse(String(options.body)); requests.push(body);
  return new Response(JSON.stringify(body.settings || {allow_sudo:true,session_isolation:false,revision}));
 }));
 render(<I18nProvider><ServerAccessSettings /></I18nProvider>);
 const user = userEvent.setup();
 await user.selectOptions(await screen.findByRole("combobox",{name:"Select server"}),"node");
 expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
 await user.selectOptions(screen.getByRole("combobox",{name:"Authentication"}),"none");
 await user.click(screen.getByRole("button",{name:"Check fingerprint"}));
 await user.click(await screen.findByRole("button",{name:"Confirm fingerprint and read settings"}));
 const sudo = await screen.findByRole("checkbox",{name:/Allow passwordless sudo/});
 expect(sudo).toBeChecked();
 expect(screen.getByRole("checkbox",{name:/Enable session isolation/})).toBeDisabled();
 await user.click(sudo);
 await user.click(screen.getByRole("checkbox",{name:/Enable session isolation/}));
 await user.click(screen.getByRole("button",{name:"Apply server settings"}));
 await waitFor(() => expect(requests).toHaveLength(2));
 expect(requests[0]).not.toHaveProperty("settings");
 expect(requests[1]).toMatchObject({host:"node.test",host_key_fingerprint:"SHA256:test",settings:{allow_sudo:false,session_isolation:true,revision}});
 expect(await screen.findByText(/Settings applied and Agent restarted/)).toBeInTheDocument();
 await user.selectOptions(screen.getByRole("combobox",{name:"Select server"}),"control");
 expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
 expect(screen.queryByRole("button",{name:"Apply server settings"})).not.toBeInTheDocument();
 expect(screen.getByText(/managed by the control-plane deployment/)).toBeInTheDocument();
});
