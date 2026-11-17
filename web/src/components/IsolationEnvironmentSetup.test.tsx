import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { IsolationEnvironmentSetup } from "./IsolationEnvironmentSetup";
import type { IsolationEnvironment } from "../data/servers";

afterEach(()=>{vi.unstubAllGlobals();localStorage.clear();});
const plan:IsolationEnvironment={plan_id:"b".repeat(64),state:"install_required",can_install:true,requires_reboot:false,packages:[{name:"bubblewrap",version:"0.11.0-1"}],checks:[{id:"bubblewrap",ok:false,detail:"missing"}]};
function mount(onChecked=vi.fn()) { localStorage.setItem("wio_language","en");return render(<I18nProvider><IsolationEnvironmentSetup id="node" disabled={false} onChecked={onChecked}/></I18nProvider>); }
function reply(env:IsolationEnvironment) {return new Response(JSON.stringify({status:"succeeded",settings:{allow_sudo:false,session_isolation:false,revision:"a".repeat(64),applied:true,restart_required:false,isolation_support:{supported:env.state==="ready"},isolation_environment:env}}));}

test("requires a concrete confirmation, installs through Agent and displays actual verified outcome",async()=>{
 const writes:unknown[]=[];const checked=vi.fn();
 vi.stubGlobal("fetch",vi.fn(async(input:RequestInfo|URL,options:RequestInit={})=>{
  const path=String(input);
  if(path.endsWith("/environment/read"))return new Response(JSON.stringify({operation_id:"plan"}));
  if(path.endsWith("/environment/install")){writes.push(JSON.parse(String(options.body)));return new Response(JSON.stringify({operation_id:"install"}));}
  return reply(path.endsWith("/install")?{...plan,state:"ready",can_install:false,logs:["Installed and verified: bubblewrap 0.11.0-1"]}:plan);
 }));
 mount(checked);const user=userEvent.setup();
 await user.click(screen.getByRole("button",{name:"Check and install isolation environment"}));
 await screen.findByText("bubblewrap 0.11.0-1");expect(writes).toEqual([]);
 await user.click(screen.getByRole("button",{name:"Install missing components…"}));expect(writes).toEqual([]);
 await user.click(screen.getByRole("button",{name:"Cancel"}));expect(writes).toEqual([]);
 await user.click(screen.getByRole("button",{name:"Install missing components…"}));
 await user.click(screen.getByRole("button",{name:"Confirm installation"}));
 expect(await screen.findByText(/The full isolation check passed/)).toBeInTheDocument();
 expect(writes).toEqual([{plan_id:plan.plan_id,request_id:expect.any(String),userspace_only:true}]);
 expect(checked).toHaveBeenLastCalledWith(expect.objectContaining({isolation_support:{supported:true}}));
 expect(localStorage.getItem("wio-isolation-install:node")).toBeNull();
});

test("keeps and retries the same confirmed request after a lost installation response",async()=>{
 const saved={plan_id:plan.plan_id,request_id:"11111111-1111-4111-8111-111111111111",userspace_only:true};
 localStorage.setItem("wio-isolation-install:node",JSON.stringify(saved));
 const writes:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(input:RequestInfo|URL,options:RequestInit={})=>{
  if(String(input).endsWith("/environment/install")){writes.push(JSON.parse(String(options.body)));if(writes.length===1)throw Error("lost response");return new Response(JSON.stringify({operation_id:"original-install"}));}
  return reply({...plan,state:"failed",can_install:false,error:"The package manager is busy.",logs:["Repository refresh failed."]});
 }));
 mount();const user=userEvent.setup();
 await user.click(await screen.findByRole("button",{name:"Continue checking installation"}));
 expect(await screen.findByText("The package manager is busy.")).toBeInTheDocument();
 expect(writes).toEqual([saved,saved]);expect(screen.queryByText(/The full isolation check passed/)).not.toBeInTheDocument();
});

test("restores the saved operation result without queuing another installation",async()=>{
 localStorage.setItem("wio-isolation-install:node",JSON.stringify({plan_id:plan.plan_id,request_id:"existing",operation_id:"saved-operation"}));
 const fetch=vi.fn(async(_input:RequestInfo|URL)=>reply({...plan,state:"failed",can_install:false,error:"The package manager is busy.",logs:["Repository refresh failed."]}));vi.stubGlobal("fetch",fetch);
 mount();
 expect(await screen.findByText("The package manager is busy.")).toBeInTheDocument();
 await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(1));
 expect(String(fetch.mock.calls[0]?.[0])).toContain("saved-operation");
 expect(screen.queryByRole("button",{name:"Confirm installation"})).not.toBeInTheDocument();
});


test("never offers an old kernel or reboot plan even if an Agent marks it installable",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(input:RequestInfo|URL)=>String(input).endsWith("/environment/read")?new Response(JSON.stringify({operation_id:"legacy-plan"})):reply({...plan,source:"debian-backports",requires_reboot:true,packages:[{name:"linux-image-amd64",version:"7.2.12-1~bpo13+1"}]})));
 mount();const user=userEvent.setup();await user.click(screen.getByRole("button",{name:"Check and install isolation environment"}));
 expect(await screen.findByText(/This plan involves kernel or system changes/)).toBeInTheDocument();
 expect(screen.queryByRole("button",{name:"Install missing components…"})).not.toBeInTheDocument();
 expect(screen.queryByText(/linux-image-amd64/)).not.toBeInTheDocument();
});

test("does not resume an unacknowledged installation from the old reboot-capable flow",async()=>{
 localStorage.setItem("wio-isolation-install:node",JSON.stringify({plan_id:plan.plan_id,request_id:"legacy",source:"debian-backports"}));
 const fetch=vi.fn();vi.stubGlobal("fetch",fetch);mount();
 expect(await screen.findByText(/previous installation plan will not be resumed/)).toBeInTheDocument();
 expect(fetch).not.toHaveBeenCalled();expect(localStorage.getItem("wio-isolation-install:node")).toBeNull();
});
