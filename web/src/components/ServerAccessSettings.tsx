import { useEffect, useRef, useState } from "react";
import { ShieldCheck } from "lucide-react";
import { useResource } from "../data/resources";
import { servers as endpoints, type AgentAccessSettings } from "../data/servers";
import { useI18n } from "../i18n";
import { Section } from "./PageUI";

export function ServerAccessSettings() {
 const { t, language } = useI18n();
 const text = (zh:string,en:string) => language === "zh-CN" ? zh : en;
 const servers = useResource(endpoints.list());
 const [id,setID] = useState("");
 const server = servers.data?.find(item => item.id === id);
 const [access,setAccess] = useState<AgentAccessSettings|null>(null);
 const [busy,setBusy] = useState(false);
 const [upgrading,setUpgrading] = useState(false);
 const [error,setError] = useState("");
 const [notice,setNotice] = useState("");
 const [reload,setReload] = useState(0);
 const generation = useRef(0);
 const pause = () => new Promise(resolve => setTimeout(resolve,500));
 async function operation(target:string,operationID:string,run:number) {
  const deadline=Date.now()+90000;
  while(generation.current===run && Date.now()<deadline) {
   const reply=await endpoints.runtimeResult(target,operationID);
   if(generation.current!==run)return null;
   if(reply.status==="succeeded" && reply.settings)return reply.settings;
   if(["failed","cancelled","canceled","interrupted"].includes(reply.status))throw new Error(reply.error||text("Agent 未能应用设置，请重试", "Agent could not apply settings; retry"));
   await pause();
  }
  if(generation.current!==run)return null;
  throw new Error(text("等待 Agent 响应超时，请重新读取当前设置", "Timed out waiting for the Agent; read the current settings again"));
 }
 async function read(target:string,run:number) {
  const queued=await endpoints.runtimeRead(target);
  if(generation.current!==run)return null;
  return operation(target,queued.operation_id,run);
 }
 useEffect(() => {
  const run=++generation.current;
  setAccess(null);setError("");setNotice("");setBusy(false);
  if(!id||!server?.agent_access_supported||server.status!=="online"||server.is_control_plane)return () => {++generation.current;};
  setBusy(true);
  void read(id,run).then(value=>{if(value&&generation.current===run)setAccess(value);}).catch(err=>{if(generation.current===run)setError(String(err));}).finally(()=>{if(generation.current===run)setBusy(false);});
  return () => {++generation.current;};
  // Server identity and availability define the lifetime of an Agent request.
  // eslint-disable-next-line react-hooks/exhaustive-deps
 },[id,server?.agent_access_supported,server?.status,reload]);
 useEffect(() => {
  if(!upgrading)return;
  const timer=setInterval(servers.reload,3000);
  if(server?.agent_access_supported)setUpgrading(false);
  return () => clearInterval(timer);
 },[upgrading,server?.agent_access_supported,servers.reload]);
 async function apply() {
  if(!access)return;
  const target=id,expected={...access},run=generation.current;
  setBusy(true);setError("");setNotice(text("正在应用设置，等待 Agent 重连确认…", "Applying settings; waiting for the Agent to reconnect…"));
  try {
   const queued=await endpoints.runtimeApply(target,expected);
   const saved=await operation(target,queued.operation_id,run);
   if(!saved)return;
   const deadline=Date.now()+90000;
   while(generation.current===run && Date.now()<deadline) {
    try {
     const actual=await read(target,run);
     if(!actual)return;
     if(actual.applied && actual.allow_sudo===expected.allow_sudo && actual.session_isolation===expected.session_isolation) {
      setAccess(actual);setNotice(text("设置已由 Agent 应用并确认生效", "Settings applied and confirmed by the Agent"));return;
     }
    } catch { /* A restart briefly disconnects the Agent; retry confirmation. */ }
    await pause();
   }
   if(generation.current===run)throw new Error(text("尚未确认 Agent 应用设置，请重新读取当前状态", "Agent settings have not been confirmed; read the current state again"));
  }catch(err){if(generation.current===run){setError(String(err));setNotice("");}}
  finally{if(generation.current===run)setBusy(false);}
 }
 async function upgrade() {
  const target=id,run=generation.current;setError("");
  try {await endpoints.updateAgent(target);if(generation.current===run){setUpgrading(true);setNotice(text("Agent 升级已排队，升级后会自动读取设置", "Agent upgrade queued; settings will load after it reconnects"));}}
  catch(err){if(generation.current===run)setError(String(err));}
 }
 return <Section title={text("服务器运行设置","Server runtime settings")} icon={<ShieldCheck size={18}/>}><div className="settings-content">
  <p>{text("选择已注册服务器，直接通过 Agent 配置 sudo 权限和会话隔离。应用设置会重启 Agent，请先结束运行中的会话。", "Select a registered server to configure sudo access and session isolation through its Agent. Applying settings restarts the Agent; finish active sessions first.")}</p>
  {servers.error && <p role="alert">{servers.error}<button onClick={servers.reload}>{t("common.retry")}</button></p>}
  <label className="field"><span>{text("选择服务器","Select server")}</span><select value={id} onChange={event=>{setID(event.target.value);setUpgrading(false);}}><option value="">{text("请选择服务器","Choose a server")}</option>{(servers.data??[]).map(item=><option key={item.id} value={item.id}>{item.is_control_plane?t("server.controlPlane"):item.name}</option>)}</select></label>
  {server?.is_control_plane ? <p>{text("内置 Agent 由控制面部署管理。会话隔离通过部署变量 WIO_CONTROL_AGENT_SESSION_ISOLATION 配置并重启生效；sudo 权限由部署环境的运行账户管理。", "The built-in Agent is managed by the control-plane deployment. Configure isolation with WIO_CONTROL_AGENT_SESSION_ISOLATION and restart the deployment. Its runtime account controls sudo access.")}</p> : server && <>
   {server.status!=="online" && <p role="status">{text("服务器离线，等待 Agent 连接后可配置", "Server offline; settings are available when the Agent connects")}</p>}
   {!server.agent_access_supported && <p>{text("请先升级此服务器的 Agent，以支持直接配置运行设置。", "Upgrade this server's Agent to configure runtime settings directly.")}{server.agent_update_available&&server.agent_update_supported&&<button disabled={upgrading||server.status!=="online"} onClick={()=>void upgrade()}>{upgrading?t("common.loading"):text("升级 Agent","Upgrade Agent")}</button>}</p>}
   <fieldset className="settings-fields" disabled={busy||!access||server.status!=="online"||!server.agent_access_supported}>
    <label className={`agent-access-option ${access?.allow_sudo?"enabled":""}`}><input type="checkbox" checked={access?.allow_sudo??false} disabled={access?.session_isolation} onChange={event=>{if(access)setAccess({...access,allow_sudo:event.target.checked});}}/><span><strong>{t("server.allowAgentSudo")}</strong><small>{t("server.allowAgentSudoWarning")}</small></span></label>
    <label className={`agent-access-option ${access?.session_isolation?"enabled":""}`}><input type="checkbox" checked={access?.session_isolation??false} disabled={access?.allow_sudo} onChange={event=>{if(access)setAccess({...access,session_isolation:event.target.checked});}}/><span><strong>{t("server.sessionIsolation")}</strong><small>{t("server.sessionIsolationHelp")}</small></span></label>
    <button className="primary-button" onClick={()=>void apply()}>{text("应用服务器设置","Apply server settings")}</button>
   </fieldset>
   {access&&!access.applied&&!busy&&<p role="status">{text("已保存的设置尚未由当前 Agent 确认生效，可重新应用。", "The running Agent has not confirmed the saved settings; apply them again to finish.")}</p>}
   <button className="secondary-button" disabled={busy||server.status!=="online"||!server.agent_access_supported} onClick={()=>setReload(value=>value+1)}>{text("重新读取设置","Read settings again")}</button>
  </>}
  {busy&&<p role="status">{t("common.loading")}</p>}
  {error&&<p role="alert">{error}</p>}
  {notice&&<p role="status">{notice}</p>}
 </div></Section>;
}
