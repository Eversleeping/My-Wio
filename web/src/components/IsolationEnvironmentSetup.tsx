import { useEffect, useRef, useState } from "react";
import { servers, type AgentAccessSettings, type IsolationEnvironment } from "../data/servers";
import { useI18n } from "../i18n";

type Pending = {plan_id:string;request_id:string;source?:"debian-backports";operation_id?:string};

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
    setPlan(result.settings.isolation_environment);checked.current(result.settings);setConfirming(false);
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
    const response=await servers.isolationInstall(id,saved.plan_id,saved.request_id,saved.source);
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
  if(!plan?.can_install||!confirming)return;
  const saved={plan_id:plan.plan_id,request_id:crypto.randomUUID(),...(plan.source?{source:plan.source}: {})};
  // Save before sending: a lost response or navigation must query/retry the
  // same confirmed request rather than queue a second package installation.
  try { localStorage.setItem(key,JSON.stringify(saved)); }
  catch {setError(text("无法保存安装记录，未开始安装。","Could not save the installation record; installation has not started."));return;}
  setPending(saved);setConfirming(false);void resume(saved,generation.current);
 }
 const stateText:Record<string,string>={
  install_required:text("缺少隔离环境，可确认安装。","Required isolation components can be installed."),
  ready:text("隔离环境已通过完整检查，可以开启会话隔离。","The full isolation check passed. You can enable session isolation."),
  reboot_required:text("内核已安装，需重启服务器后重新检测。尚未确认隔离可用；重启 Agent 无法加载新内核。","Kernel installed. Restart the server and check again; isolation has not yet passed verification. Restarting the Agent does not load a new kernel."),
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
   {plan.packages.length>0&&<><p>{plan.source==="debian-backports"?text("安装来源：Debian 官方 trixie-backports（https://deb.debian.org/debian）。使用系统 Debian 签名密钥校验，内核及必要依赖可来自该源；临时读取软件源，永久软件源配置不变。","Source: official Debian trixie-backports (https://deb.debian.org/debian). Verified with the system Debian archive keyring using a temporary catalog. The kernel and required dependencies may come from this source; permanent repositories remain unchanged."):text("安装方案使用服务器现有软件源：","Installation uses the server's configured repositories:")}</p><ul>{plan.packages.map(pkg=><li key={pkg.name}>{pkg.name} {pkg.version}</li>)}</ul></>}
   {plan.requires_reboot&&plan.can_install&&<p>{text("包含内核安装或更新，完成后需要重启整台服务器并重新验证，不能保证当前软件源的内核具备所需能力。不会自动重启。","Includes a kernel install or update. A server reboot and recheck are required; the repository's kernel may still lack the required features. The server will not restart automatically.")}</p>}
   {plan.can_install&&!pending&&!confirming&&<button className="primary-button" disabled={disabled||busy} onClick={()=>setConfirming(true)}>{text("安装缺失环境…","Install missing components…")}</button>}
   {confirming&&<div role="group" aria-label={text("确认安装隔离环境","Confirm isolation installation")}>
    <p>{text("是否通过此服务器的 Agent 安装以上软件包？安装会修改系统软件；sudo 和隔离开关保持原值。","Install these packages through this server's Agent? This changes system software. Sudo and isolation settings remain unchanged.")}</p>
    <button className="primary-button" disabled={disabled||busy} onClick={install}>{text("确认安装","Confirm installation")}</button>
    <button className="secondary-button" disabled={busy} onClick={()=>setConfirming(false)}>{text("取消","Cancel")}</button>
   </div>}
   {!!plan.logs?.length&&<details open><summary>{text("安装结果","Installation result")}</summary><ul>{plan.logs.map((line,i)=><li key={i}>{line}</li>)}</ul></details>}
  </div>}
  {error&&<p role="alert">{error}</p>}
 </div>;
}
