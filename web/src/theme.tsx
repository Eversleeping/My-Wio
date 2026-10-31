import { createContext, useContext, useEffect, useState, useMemo, useCallback, ReactNode } from "react";
import { Moon, Sun } from "lucide-react";
import { useI18n } from "./i18n";

export type Theme = "dark" | "light";

interface ThemeContextType {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  toggleTheme: () => void;
}

const ThemeContext = createContext<ThemeContextType | undefined>(undefined);

const THEME_STORAGE_KEY = "wio_theme";

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(() => {
    try {
      const saved = window.localStorage.getItem(THEME_STORAGE_KEY);
      if (saved === "light" || saved === "dark") return saved;
    } catch {}
    return "dark";
  });

  useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
    document.body.setAttribute("data-theme", theme);
    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, theme);
    } catch {}
  }, [theme]);

  const setTheme = useCallback((next: Theme) => {
    setThemeState(next);
  }, []);

  const toggleTheme = useCallback(() => {
    setThemeState(prev => (prev === "dark" ? "light" : "dark"));
  }, []);

  const value = useMemo(() => ({ theme, setTheme, toggleTheme }), [theme, setTheme, toggleTheme]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  const context = useContext(ThemeContext);
  if (!context) {
    return {
      theme: "dark" as Theme,
      setTheme: () => {},
      toggleTheme: () => {},
    };
  }
  return context;
}

export function ThemeSwitch() {
  const { theme, setTheme } = useTheme();
  const { t } = useI18n();
  return (
    <div className="theme-switch" role="group" aria-label={t("theme.theme")}>
      <button
        aria-pressed={theme === "dark"}
        className={theme === "dark" ? "active" : ""}
        onClick={() => setTheme("dark")}
        title={t("theme.dark")}
        type="button"
      >
        <Moon size={14} />
      </button>
      <button
        aria-pressed={theme === "light"}
        className={theme === "light" ? "active" : ""}
        onClick={() => setTheme("light")}
        title={t("theme.light")}
        type="button"
      >
        <Sun size={14} />
      </button>
    </div>
  );
}
