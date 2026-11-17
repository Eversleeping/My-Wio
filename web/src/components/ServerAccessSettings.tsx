import { useState, useRef, type ReactNode } from "react";
import { ShieldCheck } from "lucide-react";
import { useResource } from "../data/resources";
import { servers as endpoints, type SSHAccess, type ServerAccessSettings as Access } from "../data/servers";
import { useI18n } from "../i18n";
import { Section } from "./PageUI";
import { SSHCommandInput } from "./SSHCommandInput";

function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="field"><span>{label}</span>{children}</label>; }

const emptySSH: SSHAccess = { host: "", port: 22, user: "root", auth_method: "private_key", password: "", private_key: "", private_key_passphrase: "", host_key_fingerprint: "" };
export function ServerAccessSettings() {
 const { t, language } = useI18n();
 const text = (zh: string, en: string) => language === "zh-CN" ? zh : en;
 const servers = useResource(endpoints.list());
 const [id, setID] = useState("");
 const selectedServer = useRef("");
 const privateKeyInput = useRef<HTMLInputElement>(null);
 const server = servers.data?.find(server => server.id === id);
 const [ssh, setSSH] = useState<SSHAccess>(emptySSH);
 const [access, setAccess] = useState<Access | null>(null);
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState("");
 const [notice, setNotice] = useState("");
 const changeSSH = (next: Partial<SSHAccess>) => { setSSH(current => ({ ...current, ...next, host_key_fingerprint: "" })); setAccess(null); setNotice(""); setError(""); };
 const probe = async () => {
  setBusy(true); setError(""); setNotice("");
  try { const key = await endpoints.probe({ host: ssh.host, port: ssh.port }); setSSH({ ...ssh, host_key_fingerprint: key.fingerprint }); }
  catch (err) { setError(String(err)); } finally { setBusy(false); }
 };
 const connect = async (apply: boolean) => {
  setBusy(true); setError(""); setNotice("");
  try {
   const next = await endpoints.access(id, { ...ssh, ...(apply && access ? { settings: access } : {}) });
   setAccess(next);
   setNotice(apply ? text("设置已应用，Agent 已重启。会话隔离状态将在重连后更新。", "Settings applied and Agent restarted. Isolation status updates after reconnection.") : "");
   if (apply) {
    setSSH(current => ({ ...current, password: "", private_key: "", private_key_passphrase: "" }));
    if (privateKeyInput.current) privateKeyInput.current.value = "";
   }
  } catch (err) { setError(String(err)); if (apply) setAccess(null); }
  finally { setBusy(false); }
 };
 return <Section title={text("服务器运行设置", "Server runtime settings")} icon={<ShieldCheck size={18} />}><div className="settings-content">
 <p>{text("选择服务器后，通过 SSH 读取并配置 sudo 权限和会话隔离。应用设置会重启 Agent，请先结束运行中的会话。", "Select a server, then read and configure sudo access and session isolation over SSH. Applying settings restarts the Agent; finish active sessions first.")}</p>
 {servers.error && <p role="alert">{servers.error}<button onClick={servers.reload}>{t("common.retry")}</button></p>}
 <Field label={text("选择服务器", "Select server")}><select value={id} disabled={busy} onChange={event => {
  const next = servers.data?.find(server => server.id === event.target.value);
  selectedServer.current = event.target.value;
  setID(event.target.value); setSSH({ ...emptySSH, host: next?.address || next?.hostname || "" }); setAccess(null); setError(""); setNotice("");
 }}><option value="">{text("请选择服务器", "Choose a server")}</option>{(servers.data ?? []).map(server => <option key={server.id} value={server.id}>{server.is_control_plane ? t("server.controlPlane") : server.name}</option>)}</select></Field>
 {server?.is_control_plane ? <p>{text("内置 Agent 由控制面部署管理。会话隔离通过部署变量 WIO_CONTROL_AGENT_SESSION_ISOLATION 配置并重启生效；sudo 权限由部署环境的运行账户管理。", "The built-in Agent is managed by the control-plane deployment. Configure isolation with WIO_CONTROL_AGENT_SESSION_ISOLATION and restart the deployment. Its runtime account controls sudo access.")}</p> : server && <form key={id} onSubmit={event => { event.preventDefault(); void probe(); }}>
 <fieldset className="settings-fields" disabled={busy}>
 <SSHCommandInput onApply={connection => changeSSH({ host: connection.host, port: connection.port, user: connection.user || ssh.user, auth_method: ssh.password || ssh.private_key ? ssh.auth_method : "none" })} />
 <div className="form-grid thirds">
 <Field label={t("server.sshHost")}><input required value={ssh.host} onChange={event => changeSSH({ host: event.target.value })} /></Field>
 <Field label={t("server.sshPort")}><input required type="number" min={1} max={65535} value={ssh.port} onChange={event => changeSSH({ port: Number(event.target.value) })} /></Field>
 <Field label={t("server.sshUser")}><input required value={ssh.user} onChange={event => changeSSH({ user: event.target.value })} /></Field>
 </div>
 <Field label={t("server.authMethod")}><select value={ssh.auth_method} onChange={event => changeSSH({ auth_method: event.target.value, password: "", private_key: "", private_key_passphrase: "" })}><option value="private_key">{t("server.authPrivateKey")}</option><option value="password">{t("server.authPassword")}</option><option value="none">{t("server.authNone")}</option></select></Field>
 {ssh.auth_method === "password" && <Field label={t("server.sshPassword")}><input type="password" autoComplete="off" value={ssh.password} onChange={event => setSSH({ ...ssh, password: event.target.value })} /></Field>}
 {ssh.auth_method === "private_key" && <><Field label={t("server.privateKeyFile")}><input ref={privateKeyInput} type="file" accept=".pem,.key,text/plain" onChange={event => { const file = event.target.files?.[0]; if (file) void file.text().then(private_key => { if (selectedServer.current === id) setSSH(current => ({ ...current, private_key })); }).catch(err => { if (selectedServer.current === id) setError(String(err)); }); }} /></Field><Field label={t("server.privateKeyPassphrase")}><input type="password" autoComplete="off" value={ssh.private_key_passphrase} onChange={event => setSSH({ ...ssh, private_key_passphrase: event.target.value })} /></Field></>}
 <button className="secondary-button" type="submit">{t("server.probeFingerprint")}</button>
 {ssh.host_key_fingerprint && <><code className="fingerprint-value">{ssh.host_key_fingerprint}</code><p>{text("读取或应用设置前，请确认此指纹与目标服务器一致。", "Confirm this fingerprint matches the target server before reading or applying settings.")}</p><button type="button" className="secondary-button" onClick={() => void connect(false)}>{text("确认指纹并读取设置", "Confirm fingerprint and read settings")}</button></>}
 {access && <>
 <label className={`agent-access-option ${access.allow_sudo ? "enabled" : ""}`}><input type="checkbox" checked={access.allow_sudo} disabled={access.session_isolation} onChange={event => setAccess({ ...access, allow_sudo: event.target.checked })} /><span><strong>{t("server.allowAgentSudo")}</strong><small>{t("server.allowAgentSudoWarning")}</small></span></label>
 <label className={`agent-access-option ${access.session_isolation ? "enabled" : ""}`}><input type="checkbox" checked={access.session_isolation} disabled={access.allow_sudo} onChange={event => setAccess({ ...access, session_isolation: event.target.checked })} /><span><strong>{t("server.sessionIsolation")}</strong><small>{t("server.sessionIsolationHelp")}</small></span></label>
 <button type="button" className="primary-button" onClick={() => void connect(true)}>{text("应用服务器设置", "Apply server settings")}</button>
 </>}
 </fieldset>
 </form>}
 {busy && <p role="status">{t("common.loading")}</p>}
 {error && <p role="alert">{error}</p>}
 {notice && <p role="status">{notice}</p>}
 </div></Section>;
}
