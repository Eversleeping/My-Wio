import { useI18n } from "../i18n";
import { LoaderCircle, AlertTriangle } from "lucide-react";
import { ReactNode } from "react";
import { type DialogProps, Dialog as AccessibleDialog } from "./Dialog";

export function LoadingScreen() {
  const { t } = useI18n();
  return <div className="auth-layout"><div className="auth-brand"><span className="brand-mark">W</span><strong>{t("app.name")}</strong></div><LoaderCircle className="spin" size={28} /></div>;
}

export function LanguageSwitch() {
  const { language, setLanguage, t } = useI18n();
  return <div className="language-switch" role="group" aria-label={t("auth.language")}><button aria-pressed={language === "zh-CN"} className={language === "zh-CN" ? "active" : ""} onClick={() => setLanguage("zh-CN")} type="button">中文</button><button aria-pressed={language === "en"} className={language === "en" ? "active" : ""} onClick={() => setLanguage("en")} type="button">EN</button></div>;
}

export function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="field"><span>{label}</span>{children}</label>; }

export function Dialog(props: Omit<DialogProps, "closeLabel">) { const { t } = useI18n(); return <AccessibleDialog {...props} closeLabel={t("common.close")} />; }

export function ErrorBanner({ text }: { text: string }) { return <div className="error-banner"><AlertTriangle size={16} />{text}</div>; }
