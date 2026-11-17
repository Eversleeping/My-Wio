import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { ServerAccessSettings } from "./ServerAccessSettings";

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });
const revision="a".repeat(64);
const node={id:"node",name:"Node",status:"online",agent_access_supported:true};
test("configures through the enrolled Agent with no SSH credentials and confirms the applied state",async()=>{
 localStorage.setItem("wio_language","en");
 let settings={allow_sudo:true,session_isolation:false,revision,applied:true,restart_required:false};
 const writes:unknown[]=[];const paths:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(input:RequestInfo|URL,options:RequestInit={})=>{
  const path=String(input);paths.push(path);
  if(path.endsWith("/servers"))return new Response(JSON.stringify([node,{id:"control",name:"Control",is_control_plane:true,status:"online"}]));
  if(path.endsWith("/access-settings/read"))return new Response(JSON.stringify({operation_id:"read"}));
  if(path.endsWith("/access-settings")&&options.method==="PUT"){settings=JSON.parse(String(options.body));writes.push(settings);return new Response(JSON.stringify({operation_id:"apply"}));}
  return new Response(JSON.stringify({status:"succeeded",settings}));
 }));
 render(<I18nProvider><ServerAccessSettings/></I18nProvider>);const user=userEvent.setup();
 await user.selectOptions(await screen.findByRole("combobox",{name:"Select server"}),"node");
 expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
 expect(screen.queryByText("Authentication")).not.toBeInTheDocument();
 expect(screen.queryByRole("button",{name:"Check fingerprint"})).not.toBeInTheDocument();
 const sudo=screen.getByRole("checkbox",{name:/Allow passwordless sudo/});
 await waitFor(()=>expect(sudo).toBeEnabled());expect(sudo).toBeChecked();
 expect(screen.getByRole("checkbox",{name:/Enable session isolation/})).toBeDisabled();
 await user.click(sudo);await user.click(screen.getByRole("checkbox",{name:/Enable session isolation/}));
 await user.click(screen.getByRole("button",{name:"Apply server settings"}));
 expect(await screen.findByText("Settings applied and confirmed by the Agent")).toBeInTheDocument();
 expect(writes).toEqual([expect.objectContaining({allow_sudo:false,session_isolation:true,revision})]);
 expect(paths.every(path=>!path.includes("/ssh/"))).toBe(true);
 await user.selectOptions(screen.getByRole("combobox",{name:"Select server"}),"control");
 expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
 expect(screen.getByText(/managed by the control-plane deployment/)).toBeInTheDocument();
});
test("keeps an offline or older Agent disabled without asking for a key",async()=>{
 localStorage.setItem("wio_language","en");
 const fetch=vi.fn(async()=>new Response(JSON.stringify([{...node,id:"legacy",name:"Legacy",agent_access_supported:false,agent_update_available:true,agent_update_supported:true},{...node,id:"offline",name:"Offline",status:"offline"}])));
 vi.stubGlobal("fetch",fetch);render(<I18nProvider><ServerAccessSettings/></I18nProvider>);const user=userEvent.setup();
 await user.selectOptions(await screen.findByRole("combobox",{name:"Select server"}),"legacy");
 expect(screen.getByRole("button",{name:"Upgrade Agent"})).toBeEnabled();
 expect(screen.getByRole("checkbox",{name:/Allow passwordless sudo/})).toBeDisabled();
 await user.selectOptions(screen.getByRole("combobox",{name:"Select server"}),"offline");
 expect(screen.getByText(/Server offline/)).toBeInTheDocument();
 expect(screen.getByRole("checkbox",{name:/Enable session isolation/})).toBeDisabled();
 expect(fetch).toHaveBeenCalledTimes(1);
});
test("ignores a response for a previously selected server",async()=>{
 localStorage.setItem("wio_language","en");let resolveRead!:(value:Response)=>void;
 vi.stubGlobal("fetch",vi.fn(async(input:RequestInfo|URL)=>{
  const path=String(input);
  if(path.endsWith("/servers"))return new Response(JSON.stringify([node,{...node,id:"other",name:"Other"}]));
  if(path.includes("/node/"))return new Promise<Response>(resolve=>{resolveRead=resolve;});
  if(path.endsWith("/read"))return new Response(JSON.stringify({operation_id:"other-read"}));
  return new Response(JSON.stringify({status:"succeeded",settings:{allow_sudo:false,session_isolation:true,revision,applied:true,restart_required:false}}));
 }));
 render(<I18nProvider><ServerAccessSettings/></I18nProvider>);const user=userEvent.setup();
 await user.selectOptions(await screen.findByRole("combobox",{name:"Select server"}),"node");
 await user.selectOptions(screen.getByRole("combobox",{name:"Select server"}),"other");
 await waitFor(()=>expect(screen.getByRole("checkbox",{name:/Enable session isolation/})).toBeChecked());
 resolveRead(new Response(JSON.stringify({operation_id:"late"})));
 await waitFor(()=>expect(screen.getByRole("checkbox",{name:/Allow passwordless sudo/})).not.toBeChecked());
});
