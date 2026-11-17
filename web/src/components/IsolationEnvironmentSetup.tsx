import { useEffect, useRef, useState } from "react";
import { servers, type AgentAccessSettings, type IsolationEnvironment } from "../data/servers";
import { useI18n } from "../i18n";

type Pending = {plan_id:string;request_id:string;source?:"debian-backports";userspace_only?:boolean;operation_id?:string};

export function IsolationEnvironmentSetup({id,disabled,onChecked,onBusy}:{id:string;disabled:boolean;onChecked:(settings:AgentAccessSettings)=>void;onBusy?:(busy:boolean)=>void}) {
 const {language}=useI18n();
 const text=(zh:string,en:string)=>language==="zh-CN"?zh:en;
 const [plan,setPlan]=useState<IsolationEnvironment|null>(null);
 const [busy,setBusy]=useState(false),[confirming,setConfirming]=useState(false),[error,setError]=useState("");
 const [pending,setPending]=useState<Pending|null>(null);
 const generation=useRef(0),checked=useRef(onChecked);checked.current=onChecked;
 const key="wio-isolation-install:"+id;
 useEffect(()=>{onBusy?.(busy||!!pending);return ()=>onBusy?.(false);},[busy,pending,onBusy]);
 async function poll(operation:string,run:number) {
  const deadline=Date.now()+31*60*1000;
  while(generation.current===run&&Date.now()<deadline) {
   const result=await servers.runtimeResult(id,operation);
   if(generation.current!==run)return;
   if(result.status==="succeeded"&&result.settings?.isolation_environment) {
    setPlan(safePlan(result.settings.isolation_environment));checked.current(result.settings);setConfirming(false);
    localStorage.removeItem(key);setPending(null);return;
   }
   if(["failed","cancelled","canceled","interrupted"].includes(result.status)) {
    localStorage.removeItem(key);setPending(null);throw Error(result.error||text("安装检查失败，请重新检测环境。","Environment operation failed; check again."));
   }
   await new Promise(resolve=>setTimeout(resolve,1000));
  }
  if(generation.current===run)throw Error(text("等待结果超时，安装可能仍在执行，请继续查询。","Timed out waiting; installation may still be running. Continue checking the same operation."));
 }
 async function resume(saved:Pending,run:number) {
  setBusy(true);setError("");
  try {
   if(!saved.operation_id) {
    if(!saved.userspace_only||saved.source){localStorage.removeItem(key);setPending(null);throw Error(text("旧安装方案已停止恢复，请重新检查并确认无需重启服务器的方案。","The previous installation plan will not be resumed. Check again and confirm a plan that needs no server reboot."));}
    const response=await servers.isolationInstall(id,saved.plan_id,saved.request_id);
    saved={...saved,operation_id:response.operation_id};
    localStorage.setItem(key,JSON.stringify(saved));
    if(generation.current!==run)return;
    setPending(saved);
   }
   await poll(saved.operation_id!,run);
  } catch(e) { if(generation.current===run)setError(String(e)); }
  finally { if(generation.current===run)setBusy(false); }
 }
 useEffect(()=>{
  const run=++generation.current;
  try {
   const raw=localStorage.getItem(key);
   if(raw) { const saved:Pending=JSON.parse(raw);if(saved.plan_id&&saved.request_id){setPending(saved);void resume(saved,run);} }
  } catch { setError(text("无法读取安装记录，请重新查询服务器状态。","Could not read the installation record; check server status.")); }
  return ()=>{++generation.current;};
  // A server change remounts this component; never mix operations across hosts.
  // eslint-disable-next-line react-hooks/exhaustive-deps
 },[id]);
 async function inspect() {
  const run=generation.current;setBusy(true);setError("");setConfirming(false);
  try { const response=await servers.isolationPlan(id);await poll(response.operation_id,run); }
  catch(e) { if(generation.current===run)setError(String(e)); }
  finally { if(generation.current===run)setBusy(false); }
 }
 function install() {
  if(!plan?.can_install||plan.requires_reboot||plan.source||plan.packages.some(pkg=>pkg.name!=="bubblewrap")||!confirming)return;
  const saved={plan_id:plan.plan_id,request_id:crypto.randomUUID(),userspace_only:true};
  // Save before sending: a lost response or navigation must query/retry the
  // same confirmed request rather than queue a second package installation.
  try { localStorage.setItem(key,JSON.stringify(saved)); }
  catch {setError(text("无法保存安装记录，未开始安装。","Could not save the installation record; installation has not started."));return;}
  setPending(saved);setConfirming(false);void resume(saved,generation.current);
 }
 function safePlan(value:IsolationEnvironment):IsolationEnvironment {
  if(value.requires_reboot||value.source||value.state==="reboot_required"||value.packages.some(pkg=>pkg.name!=="bubblewrap"))return {...value,state:"blocked",can_install:false,requires_reboot:false,source:undefined,packages:[],logs:[],error:text("该方案涉及内核、系统升级或服务器重启，已禁止安装。仅支持无需重启服务器的隔离工具安装。","This plan involves kernel or system changes or a server reboot and cannot be installed. Only isolation tools that need no server reboot are supported.")};
  return value;
 }
 const stateText:Record<string,string>={
  install_required:text("缺少隔离环境，可确认安装。","Required isolation components can be installed."),
  ready:text("隔离环境已通过完整检查，可以开启会话隔离。","The full isolation check passed. You can enable session isolation."),
  blocked:text("环境检查未通过，以下问题仍需处理。","The environment check found requirements that still need attention."),
  failed:text("安装未完成，请查看结果后重新检测。","Installation did not complete. Review the result and check again."),
 };
 return <div className="isolation-environment-setup">
  <button className="secondary-button" disabled={disabled||busy||!!pending} onClick={()=>void inspect()}>{text("检查并安装隔离环境","Check and install isolation environment")}</button>
  {busy&&<p role="status">{pending?text("Agent 正在安装并验证，请稍候；可离开页面后继续查询。","The Agent is installing and verifying. You can return later to check the result."):text("正在检查服务器环境并生成安装方案…","Checking the server and preparing an installation plan…")}</p>}
  {pending&&!busy&&<button className="secondary-button" disabled={disabled} onClick={()=>void resume(pending,generation.current)}>{text("继续查询安装结果","Continue checking installation")}</button>}
  {plan&&<div aria-label={text("隔离环境结果","Isolation environment result")}>
   <p role="status">{stateText[plan.state]||plan.state}</p>
   <ul>{plan.checks.map(check=><li key={check.id}>{check.ok?"✓ ":"✗ "}{check.id==="agent_boundary"?text("Agent 完整隔离检查","Full Agent isolation check"):check.id==="landlock"?text("内核隔离能力","Kernel isolation support"):text("隔离工具","Isolation tools")}{check.detail&&`: ${check.detail}`}</li>)}</ul>
   {plan.error&&<p role="alert">{plan.error}</p>}
   {plan.packages.length>0&&<><p>{text("以下工具可从服务器现有软件源安装，无需重启服务器：","These tools can be installed from the server's configured repositories without a server reboot:")}</p><ul>{plan.packages.map(pkg=><li key={pkg.name}>{pkg.name} {pkg.version}</li>)}</ul></>}
   {plan.can_install&&!pending&&!confirming&&<button className="primary-button" disabled={disabled||busy} onClick={()=>setConfirming(true)}>{text("安装缺失环境…","Install missing components…")}</button>}
   {confirming&&<div role="group" aria-label={text("确认安装隔离环境","Confirm isolation installation")}>
    <p>{text("是否通过此服务器的 Agent 安装以上软件包？仅安装以上工具，无需重启服务器；sudo 和隔离开关保持原值。","Install these packages through this server's Agent? Only the listed tools will be installed; no server reboot is needed. Sudo and isolation settings remain unchanged.")}</p>
    <button className="primary-button" disabled={disabled||busy} onClick={install}>{text("确认安装","Confirm installation")}</button>
    <button className="secondary-button" disabled={busy} onClick={()=>setConfirming(false)}>{text("取消","Cancel")}</button>
   </div>}
   {!!plan.logs?.length&&<details open><summary>{text("安装结果","Installation result")}</summary><ul>{plan.logs.map((line,i)=><li key={i}>{line}</li>)}</ul></details>}
  </div>}
  {error&&<p role="alert">{error}</p>}
 </div>;
}
