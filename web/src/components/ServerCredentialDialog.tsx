import { type FormEvent, useEffect, useRef, useState } from "react";
import { KeyRound, LoaderCircle } from "lucide-react";
import { api, post } from "../api";
import { useI18n } from "../i18n";
import type { CredentialProfile, Server } from "../types";
import { Dialog, DialogActions } from "./Dialog";

type Operation = { id: string; status: string; result: string };
const pending = (status: string) => ["queued", "delivered", "running"].includes(status);
const message = (error: unknown) => error instanceof Error ? error.message : String(error);

export function ServerCredentialDialog({ server, profiles, profilesError, onClose, onApplied, notify }: {
  server: Server; profiles: CredentialProfile[]; profilesError: string;
  onClose: () => void; onApplied: () => void; notify: (text: string) => void;
}) {
  const { t } = useI18n();
  const codexProfiles = profiles.filter(profile => profile.kind === "codex");
  const geminiProfiles = profiles.filter(profile => profile.kind === "gemini");
  const claudeProfiles = profiles.filter(profile => profile.kind === "claude");
  const gitProfiles = profiles.filter(profile => profile.kind === "git");
  const [kind, setKind] = useState<"codex" | "git" | "gemini" | "claude">(server.codex_profile_id || codexProfiles.length ? "codex" : gitProfiles.length ? "git" : geminiProfiles.length ? "gemini" : claudeProfiles.length ? "claude" : "codex");
  const [codexProfileID, setCodexProfileID] = useState(server.codex_profile_id);
  const [geminiProfileID, setGeminiProfileID] = useState(server.gemini_profile_id || "");
  const [claudeProfileID, setClaudeProfileID] = useState(server.claude_profile_id || "");
  const [gitProfileIDs, setGitProfileIDs] = useState(server.git_profiles?.map(profile => profile.id) ?? (server.git_profile_id ? [server.git_profile_id] : []));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [operation, setOperation] = useState<Operation | null>(null);
  const submitRef = useRef(false);
  const applying = busy || !!operation && pending(operation.status);
  const unsupported = ((kind === "codex" || kind === "git") && !server.scoped_credentials_supported) || kind === "gemini" && !server.gemini_credentials_supported || kind === "claude" && !server.claude_credentials_supported;
  const selectedProfileID = kind === "gemini" ? geminiProfileID : kind === "claude" ? claudeProfileID : codexProfileID;

  useEffect(() => {
    if (!operation || !pending(operation.status)) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const result = await api<Operation>(`/operations/${operation.id}`, { signal: controller.signal });
        if (controller.signal.aborted) return;
        setOperation(result); setError("");
        if (!pending(result.status)) { onApplied(); return; }
      } catch (failure) { if (!controller.signal.aborted) setError(message(failure)); }
      if (!controller.signal.aborted) timer = setTimeout(poll, 2000);
    };
    void poll();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [operation?.id, operation?.status, onApplied]);

  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (submitRef.current || applying || unsupported || server.status !== "online" || (kind !== "git" && !selectedProfileID)) return;
    submitRef.current = true; setBusy(true); setError(""); setOperation(null);
    try {
      const result = kind === "gemini" || kind === "claude"
        ? await post<{ operation_id: string }>(`/servers/${server.id}/${kind}-credential-profile`, { profile_id: selectedProfileID })
        : await post<{ operation_id: string }>(`/servers/${server.id}/credential-profiles`, kind === "git" ? { scope: "git", git_profile_ids: gitProfileIDs } : { scope: "codex", codex_profile_id: codexProfileID });
      setOperation({ id: result.operation_id, status: "queued", result: "" });
    } catch (failure) { setError(message(failure)); }
    finally { setBusy(false); submitRef.current = false; }
  };
  const selectKind = (value: "codex" | "git" | "gemini" | "claude") => { setKind(value); setError(""); setOperation(null); };

  return <Dialog open title={t("server.editCredentials")} onClose={onClose} dismissDisabled={busy} closeLabel={t("common.close")}>
    <form onSubmit={save}>
      <p className="security-notice">{t("server.credentialsDescription", { name: server.name })}</p>
      <div className="segmented-control server-credential-tabs" role="tablist" aria-label={t("settings.type")}>
        <button type="button" role="tab" aria-selected={kind === "codex"} className={kind === "codex" ? "active" : ""} disabled={applying} onClick={() => selectKind("codex")}>{t("settings.codexType")}</button>
        <button type="button" role="tab" aria-selected={kind === "git"} className={kind === "git" ? "active" : ""} disabled={applying} onClick={() => selectKind("git")}>Git</button>
        <button type="button" role="tab" aria-selected={kind === "gemini"} className={kind === "gemini" ? "active" : ""} disabled={applying} onClick={() => selectKind("gemini")}>{t("settings.geminiType")}</button>
        <button type="button" role="tab" aria-selected={kind === "claude"} className={kind === "claude" ? "active" : ""} disabled={applying} onClick={() => selectKind("claude")}>{t("settings.claudeType")}</button>
      </div>
      {profilesError && <p className="error-banner" role="alert">{profilesError}</p>}
      {kind === "gemini" ? <>
        <p className="muted">{t("settings.geminiCurrentProfile", { name: server.gemini_profile_name || t("settings.geminiUnconfigured") })}</p>
        <label className="field"><span>{t("settings.geminiType")}</span><select value={geminiProfileID} required disabled={applying || unsupported} onChange={event => { setGeminiProfileID(event.target.value); setOperation(null); }}>
          <option value="">{t("settings.selectGeminiProfile")}</option>{geminiProfiles.map(profile => <option key={profile.id} value={profile.id}>{profile.name} · {profile.model}</option>)}
        </select></label>
        {!geminiProfiles.length && <p className="security-notice">{t("settings.noGeminiProfiles")}</p>}
        {unsupported && <p className="security-notice">{t("settings.geminiUpgradeAgent")}</p>}
      </> : kind === "claude" ? <>
        <p className="muted">{t("settings.claudeCurrentProfile", { name: server.claude_profile_name || t("settings.geminiUnconfigured") })}</p>
        <label className="field"><span>{t("settings.claudeType")}</span><select value={claudeProfileID} required disabled={applying || unsupported} onChange={event => { setClaudeProfileID(event.target.value); setOperation(null); }}>
          <option value="">{t("settings.selectClaudeProfile")}</option>{claudeProfiles.map(profile => <option key={profile.id} value={profile.id}>{profile.name} · {profile.model}</option>)}
        </select></label>
        {!claudeProfiles.length && <p className="security-notice">{t("settings.noClaudeProfiles")}</p>}
        {unsupported && <p className="security-notice">{t("settings.claudeUpgradeAgent")}</p>}
      </> : kind === "codex" ? <>
        <label className="field"><span>{t("server.codexProfile")}</span><select value={codexProfileID} required disabled={applying || unsupported} onChange={event => setCodexProfileID(event.target.value)}>
          <option value="">{t(codexProfiles.length ? "server.selectCodexProfile" : "server.noCodexProfiles")}</option>{codexProfiles.map(profile => <option value={profile.id} key={profile.id}>{profile.name} · {profile.model}</option>)}
        </select></label>
      </> : null}
      {kind === "git" && <>
        <div className="field"><span>{t("server.gitProfiles")}</span><div className="credential-profile-options">
          {!gitProfiles.length && <span className="muted">{t("server.noGitProfile")}</span>}{gitProfiles.map(profile => {
            const ready = Boolean(profile.commit_name && profile.commit_email);
            const checked = gitProfileIDs.includes(profile.id);
            return <label className={`credential-profile-option ${checked ? "selected" : ""} ${ready ? "" : "disabled"}`} key={profile.id}><input type="checkbox" checked={checked} disabled={applying || unsupported || !ready} onChange={event => setGitProfileIDs(current => event.target.checked ? [...current, profile.id] : current.filter(id => id !== profile.id))} /><span><strong>{profile.name}</strong><small>{profile.username}{ready ? "" : ` · ${t("settings.gitIdentityMissing")}`}</small></span></label>;
          })}
        </div></div>
        {!gitProfileIDs.length && <p className="security-notice">{t("server.gitCredentialsEmpty")}</p>}
      </>}
      {(kind === "codex" || kind === "git") && unsupported && <p className="security-notice">{t("server.scopedCredentialsUpgrade")}</p>}
      {server.status !== "online" && <p className="error-banner" role="alert">{t("server.credentialsOffline")}</p>}
      {error && <p className="error-banner" role="alert">{error}</p>}
      {operation && <p role="status" className={operation.status === "failed" ? "error-banner" : "security-notice"}>{t(`status.${operation.status}`)}{operation.result ? ` · ${operation.result}` : ""}</p>}
      <DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{t("common.close")}</button><button className="primary-button" disabled={applying || unsupported || server.status !== "online" || !!profilesError || (kind !== "git" && !selectedProfileID)}>{applying ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}{t(applying ? "server.credentialsUpdating" : "server.saveCredentials")}</button></DialogActions>
    </form>
  </Dialog>;
}
