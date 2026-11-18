import { useEffect, useRef, useState } from "react";
import { ShieldCheck } from "lucide-react";
import { useResource } from "../data/resources";
import { servers as endpoints, type AgentAccessSettings } from "../data/servers";
import { useI18n } from "../i18n";
import { Section } from "./PageUI";

export function ServerAccessSettings() {
 const { t, language } = useI18n();
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
   if(["failed","cancelled","canceled","interrupted"].includes(reply.status))throw new Error(reply.error||t("server.runtime.applyFailed"));
   await pause();
  }
  if(generation.current!==run)return null;
  throw new Error(t("server.runtime.responseTimeout"));
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
  if(server&&!server.agent_update_available){setUpgrading(false);setReload(value=>value+1);}
  return () => clearInterval(timer);
 },[upgrading,server?.agent_update_available,servers.reload]);
 async function apply() {
  if(!access)return;
  const target=id,expected={...access},run=generation.current;
  setBusy(true);setError("");setNotice(t("server.runtime.applying"));
  try {
   const queued=await endpoints.runtimeApply(target,expected);
   const saved=await operation(target,queued.operation_id,run);
   if(!saved)return;
   const deadline=Date.now()+90000;
   while(generation.current===run && Date.now()<deadline) {
    try {
     const actual=await read(target,run);
     if(!actual)return;
     if(actual.applied && !actual.restart_required && actual.allow_sudo===expected.allow_sudo) {
      setAccess(actual);setNotice(t("server.runtime.applied"));return;
     }
    } catch { /* A restart briefly disconnects the Agent; retry confirmation. */ }
    await pause();
   }
   if(generation.current===run)throw new Error(t("server.runtime.applyUnconfirmed"));
  }catch(err){if(generation.current===run){setError(String(err));setNotice("");}}
  finally{if(generation.current===run)setBusy(false);}
 }
 async function upgrade() {
  const target=id,run=generation.current;setError("");
  try {await endpoints.updateAgent(target);if(generation.current===run){setUpgrading(true);setNotice(t("server.runtime.upgradeQueued"));}}
  catch(err){if(generation.current===run)setError(String(err));}
 }
 async function restartAgent() {
  const target=id,run=generation.current;
  setBusy(true);setError("");setNotice(t("server.runtime.restarting"));
  try {
   const queued=await endpoints.restartAgent(target);
   const before=await operation(target,queued.operation_id,run);
   if(!before)return;
   if(!before.instance_id)throw new Error(t("server.runtime.restartRequiresUpgrade"));
   const deadline=Date.now()+90000;
   while(generation.current===run&&Date.now()<deadline) {
    try {
     const actual=await read(target,run);
     if(!actual)return;
     if(actual.instance_id&&actual.instance_id!==before.instance_id&&actual.applied&&!actual.restart_required) {
      setAccess(actual);setNotice(t("server.runtime.restarted"));servers.reload();return;
     }
    } catch { /* The service disconnects while restarting; retry confirmation. */ }
    await pause();
   }
   if(generation.current===run)throw new Error(t("server.runtime.restartUnconfirmed"));
  }catch(err){if(generation.current===run){setError(String(err));setNotice("");}}
  finally{if(generation.current===run)setBusy(false);}
 }
 return <Section title={t("server.runtime.title")} icon={<ShieldCheck size={18}/>}><div className="settings-content">
  <p>{t("server.runtime.instructions")}</p>
  {servers.error && <p role="alert">{servers.error}<button onClick={servers.reload}>{t("common.retry")}</button></p>}
  <label className="field"><span>{t("server.runtime.server")}</span><select value={id} onChange={event=>{setID(event.target.value);setUpgrading(false);}}><option value="">{t("server.runtime.chooseServer")}</option>{(servers.data??[]).map(item=><option key={item.id} value={item.id}>{item.is_control_plane?t("server.controlPlane"):item.name}</option>)}</select></label>
  {server?.is_control_plane ? <p>{t("server.runtime.builtIn")}</p> : server && <>
   {server.status!=="online" && <p role="status">{t("server.runtime.offline")}</p>}
   {!server.agent_access_supported && <p>{t("server.runtime.upgradeRequired")}</p>}
   {server.agent_update_available&&server.agent_update_supported&&<button disabled={upgrading||busy||server.status!=="online"} onClick={()=>void upgrade()}>{upgrading?t("common.loading"):t("server.runtime.upgrade")}</button>}
   <fieldset className="settings-fields" disabled={busy||upgrading||!access||server.status!=="online"||!server.agent_access_supported}>
    <label className={`agent-access-option ${access?.allow_sudo?"enabled":""}`}><input type="checkbox" checked={access?.allow_sudo??false} disabled={access?.restart_required} onChange={event=>{if(access)setAccess({...access,allow_sudo:event.target.checked});}}/><span><strong>{t("server.allowAgentSudo")}</strong><small>{t("server.allowAgentSudoWarning")}</small></span></label>
    <button className="primary-button" onClick={()=>void apply()}>{t("server.runtime.apply")}</button>
   </fieldset>
   {access?.restart_required&&!busy&&<p role="status">{t("server.runtime.restartPending")}</p>}
   {access&&!access.applied&&!access.restart_required&&!busy&&<p role="status">{t("server.runtime.savedUnconfirmed")}</p>}
   <button className="secondary-button" disabled={busy||upgrading||server.status!=="online"||!server.agent_restart_supported} onClick={()=>void restartAgent()}>{t("server.runtime.restart")}</button>
   {!server.agent_restart_supported&&server.agent_access_supported&&<p>{t("server.runtime.upgradeForRestart")}</p>}
   <button className="secondary-button" disabled={busy||server.status!=="online"||!server.agent_access_supported} onClick={()=>setReload(value=>value+1)}>{t("server.runtime.readAgain")}</button>
  </>}
  {busy&&<p role="status">{t("common.loading")}</p>}
  {error&&<p role="alert">{error}</p>}
  {notice&&<p role="status">{notice}</p>}
 </div></Section>;
}
