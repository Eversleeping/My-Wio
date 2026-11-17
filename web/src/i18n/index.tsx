import { createContext, ReactNode, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { en, zh } from "./messages";

export type Language = "zh-CN" | "en";
type Messages = Record<string, string>;

const dictionaries: Record<Language, Messages> = { "zh-CN": zh, en };
let activeLanguage: Language = "zh-CN";

interface I18nValue {
  language: Language;
  setLanguage: (language: Language) => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}

const I18nContext = createContext<I18nValue | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const stored = window.localStorage.getItem("wio_language");
  const [language, updateLanguage] = useState<Language>(stored === "en" ? "en" : "zh-CN");
  activeLanguage = language;
  useEffect(() => {
    document.documentElement.lang = language;
    document.title = dictionaries[language]["app.name"];
    document.querySelector<HTMLMetaElement>('meta[name="description"]')?.setAttribute("content", dictionaries[language]["app.description"]);
  }, [language]);
  const setLanguage = useCallback((next: Language) => {
    window.localStorage.setItem("wio_language", next);
    activeLanguage = next;
    updateLanguage(next);
    document.documentElement.lang = next;
  }, []);
  const t = useCallback((key: string, values: Record<string, string | number> = {}) => {
    let result = dictionaries[language][key] ?? dictionaries.en[key] ?? key;
    for (const [name, value] of Object.entries(values)) result = result.replaceAll(`{${name}}`, String(value));
    return result;
  }, [language]);
  const value = useMemo(() => ({ language, setLanguage, t }), [language, setLanguage, t]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n() {
  const value = useContext(I18nContext);
  if (!value) throw new Error("I18nProvider is missing");
  return value;
}

export function currentLocale() {
  return activeLanguage === "zh-CN" ? "zh-CN" : "en";
}
