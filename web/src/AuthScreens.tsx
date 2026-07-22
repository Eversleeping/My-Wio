import { FormEvent, ReactNode, useId, useState } from "react";
import { AlertTriangle, Check, ChevronRight, Copy, Eye, EyeOff, KeyRound, LoaderCircle, LockKeyhole, ShieldCheck } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { post } from "./api";
import { useI18n } from "./i18n";
import type { Session } from "./types";

export type AuthMode = "password" | "totp" | "password_totp";

type SetupResult = {
  username: string;
  auth_mode: AuthMode;
  totp_uri?: string;
  totp_secret?: string;
  recovery_codes?: string[];
};

const authModes: Array<{ value: AuthMode; title: string; description: string }> = [
  { value: "password", title: "auth.modePassword", description: "auth.modePasswordDescription" },
  { value: "totp", title: "auth.modeTOTP", description: "auth.modeTOTPDescription" },
  { value: "password_totp", title: "auth.modePasswordTOTP", description: "auth.modePasswordTOTPDescription" }
];

function needsPassword(mode: AuthMode) {
  return mode === "password" || mode === "password_totp";
}

function needsCode(mode: AuthMode) {
  return mode === "totp" || mode === "password_totp";
}

export function SetupScreen({ onReady }: { onReady: (authMode: AuthMode) => void }) {
  const { t } = useI18n();
  const [username, setUsername] = useState("admin");
  const [authMode, setAuthMode] = useState<AuthMode>("totp");
  const [password, setPassword] = useState("");
  const [passwordConfirmation, setPasswordConfirmation] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmation, setShowConfirmation] = useState(false);
  const [result, setResult] = useState<SetupResult | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (needsPassword(authMode) && password !== passwordConfirmation) {
      setError(t("auth.passwordMismatch"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const payload: Record<string, string> = { username, auth_mode: authMode };
      if (needsPassword(authMode)) payload.password = password;
      setResult(await post<SetupResult>("/setup", payload));
    } catch (err) {
      setError(err instanceof Error ? err.message : t("error.request"));
    } finally {
      setBusy(false);
    }
  };

  const copyRecoveryCodes = async () => {
    const codes = result?.recovery_codes ?? [];
    if (!codes.length || !navigator.clipboard) return;
    await navigator.clipboard.writeText(codes.join("\n"));
  };

  return <div className="auth-layout">
    <section className={`auth-panel ${result ? "auth-panel-complete" : "auth-panel-setup"}`}>
      <AuthBrand />
      {!result ? <form onSubmit={submit}>
        <div className="auth-heading-copy">
          <div className="section-heading"><h1>{t("auth.createAdmin")}</h1><span className="status-tag neutral"><LockKeyhole size={14} />{t("auth.singleAdmin")}</span></div>
          <p>{t("auth.setupIntro")}</p>
        </div>
        <Field label={t("auth.username")}><input value={username} onChange={event => setUsername(event.target.value)} autoComplete="username" required /></Field>
        <fieldset className="auth-method-fieldset">
          <legend>{t("auth.methodLabel")}</legend>
          <div className="auth-methods" role="radiogroup" aria-label={t("auth.methodLabel")}>
            {authModes.map(mode => <label key={mode.value} className={`auth-method-option ${authMode === mode.value ? "selected" : ""}`}>
              <input type="radio" name="auth-mode" value={mode.value} checked={authMode === mode.value} onChange={() => setAuthMode(mode.value)} />
              <span className="auth-method-copy"><strong>{t(mode.title)}</strong><small>{t(mode.description)}</small></span>
            </label>)}
          </div>
        </fieldset>
        {needsPassword(authMode) && <div className="auth-password-fields">
          <PasswordField label={t("auth.password")} value={password} visible={showPassword} onChange={setPassword} onToggle={() => setShowPassword(value => !value)} autoComplete="new-password" showLabel={t("auth.showPassword")} hideLabel={t("auth.hidePassword")} />
          <PasswordField label={t("auth.confirmPassword")} value={passwordConfirmation} visible={showConfirmation} onChange={setPasswordConfirmation} onToggle={() => setShowConfirmation(value => !value)} autoComplete="new-password" showLabel={t("auth.showPassword")} hideLabel={t("auth.hidePassword")} />
          <p className="auth-help">{t("auth.passwordRequirement")}</p>
        </div>}
        {error && <ErrorBanner text={error} />}
        <button className="primary-button full" disabled={busy}>{busy ? <LoaderCircle className="spin" size={17} /> : <ShieldCheck size={17} />}{t("auth.createAdmin")}</button>
      </form> : <div>
        <div className="section-heading"><h1>{result.totp_secret ? t("auth.twoFactor") : t("auth.adminReady")}</h1><span className="status-tag success"><Check size={14} />{t("auth.ready")}</span></div>
        {result.totp_secret ? <>
          <p className="auth-complete-intro">{t("auth.twoFactorIntro")}</p>
          <div className="totp-grid"><div className="qr"><QRCodeSVG value={result.totp_uri ?? ""} size={156} /></div><div><Field label={t("auth.totpSecret")}><code className="secret-code">{result.totp_secret}</code></Field><div className="auth-recovery-heading"><p className="label">{t("auth.recoveryCodes")}</p><button type="button" className="icon-button" title={t("auth.copyRecoveryCodes")} aria-label={t("auth.copyRecoveryCodes")} onClick={() => void copyRecoveryCodes()}><Copy size={15} /></button></div><div className="recovery-codes">{(result.recovery_codes ?? []).map(code => <code key={code}>{code}</code>)}</div></div></div>
          <p className="auth-security-note"><KeyRound size={15} />{t("auth.recoveryCodesNote")}</p>
        </> : <p className="auth-complete-intro">{t("auth.passwordOnlyIntro")}</p>}
        <button className="primary-button full" onClick={() => onReady(result.auth_mode)}><ChevronRight size={17} />{t("auth.continue")}</button>
      </div>}
    </section>
  </div>;
}

export function LoginScreen({ authMode, onLogin }: { authMode: AuthMode; onLogin: (session: Session) => void }) {
  const { t } = useI18n();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      onLogin(await post<Session>("/auth/login", { username, password, code }));
    } catch (err) {
      setError(err instanceof Error ? err.message : t("error.request"));
    } finally {
      setBusy(false);
    }
  };
  return <div className="auth-layout"><section className="auth-panel compact">
    <AuthBrand />
    <form onSubmit={submit}><h1>{t("auth.signIn")}</h1><p className="auth-login-mode">{t(`auth.loginMode.${authMode}`)}</p>
      <Field label={t("auth.username")}><input value={username} onChange={event => setUsername(event.target.value)} autoComplete="username" required /></Field>
      {needsPassword(authMode) && <PasswordField label={t("auth.password")} value={password} visible={showPassword} onChange={setPassword} onToggle={() => setShowPassword(value => !value)} autoComplete="current-password" showLabel={t("auth.showPassword")} hideLabel={t("auth.hidePassword")} />}
      {needsCode(authMode) && <Field label={t("auth.code")}><input value={code} onChange={event => setCode(event.target.value)} inputMode="text" autoCapitalize="characters" autoComplete="one-time-code" required /></Field>}
      {error && <ErrorBanner text={error} />}
      <button className="primary-button full" disabled={busy}>{busy ? <LoaderCircle className="spin" size={17} /> : <LockKeyhole size={17} />}{t("auth.signIn")}</button>
    </form>
  </section></div>;
}

function AuthBrand() {
  const { t } = useI18n();
  return <div className="auth-brand"><span className="brand-mark">W</span><strong>{t("app.name")}</strong><LanguageSwitch /></div>;
}

function LanguageSwitch() {
  const { language, setLanguage, t } = useI18n();
  return <div className="language-switch" role="group" aria-label={t("auth.language")}><button aria-pressed={language === "zh-CN"} className={language === "zh-CN" ? "active" : ""} onClick={() => setLanguage("zh-CN")} type="button">中文</button><button aria-pressed={language === "en"} className={language === "en" ? "active" : ""} onClick={() => setLanguage("en")} type="button">EN</button></div>;
}

function PasswordField({ label, value, visible, onChange, onToggle, autoComplete, showLabel, hideLabel }: { label: string; value: string; visible: boolean; onChange: (value: string) => void; onToggle: () => void; autoComplete: string; showLabel: string; hideLabel: string }) {
  const id = useId();
  const toggleLabel = visible ? hideLabel : showLabel;
  return <div className="field"><label htmlFor={id}>{label}</label><div className="password-input"><input id={id} type={visible ? "text" : "password"} value={value} onChange={event => onChange(event.target.value)} autoComplete={autoComplete} minLength={12} required /><button type="button" className="icon-button" title={toggleLabel} aria-label={toggleLabel} onClick={onToggle}>{visible ? <EyeOff size={16} /> : <Eye size={16} />}</button></div></div>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}</label>;
}

function ErrorBanner({ text }: { text: string }) {
  return <div className="error-banner"><AlertTriangle size={16} />{text}</div>;
}
