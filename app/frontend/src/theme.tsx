// Dual light/dark theme. The .dark class on <html> is set pre-paint by the inline
// script in index.html (anti-FOUC); this provider keeps it in sync and persists a
// three-way preference (light | dark | system) in localStorage. No timers.
import { createContext, useContext, useEffect, useState, useCallback, ReactNode } from "react";
import { Sun, Moon, Monitor } from "lucide-react";
import { cx } from "./ui";

export type Theme = "light" | "dark" | "system";
const KEY = "theme";
const mql = () => window.matchMedia("(prefers-color-scheme: dark)");

function resolve(t: Theme): boolean {
  return t === "dark" || (t === "system" && mql().matches);
}
function apply(t: Theme) {
  document.documentElement.classList.toggle("dark", resolve(t));
}

interface ThemeApi { theme: Theme; setTheme: (t: Theme) => void; isDark: boolean }
const ThemeCtx = createContext<ThemeApi>({ theme: "system", setTheme: () => {}, isDark: false });
export const useTheme = () => useContext(ThemeCtx);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(() => (localStorage.getItem(KEY) as Theme) || "system");
  const [isDark, setIsDark] = useState(() => resolve(theme));

  const setTheme = useCallback((t: Theme) => {
    setThemeState(t);
    if (t === "system") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, t);
    apply(t);
    setIsDark(resolve(t));
  }, []);

  // live-update when in "system" mode and the OS preference flips
  useEffect(() => {
    const m = mql();
    const onChange = () => { if (((localStorage.getItem(KEY) as Theme) || "system") === "system") { apply("system"); setIsDark(mql().matches); } };
    m.addEventListener("change", onChange);
    return () => m.removeEventListener("change", onChange);
  }, []);

  return <ThemeCtx.Provider value={{ theme, setTheme, isDark }}>{children}</ThemeCtx.Provider>;
}

const order: Theme[] = ["light", "dark", "system"];
const meta: Record<Theme, { icon: typeof Sun; label: string }> = {
  light: { icon: Sun, label: "Light" },
  dark: { icon: Moon, label: "Dark" },
  system: { icon: Monitor, label: "System" },
};

// ThemeToggle cycles light → dark → system, showing the current mode's icon.
export function ThemeToggle({ className }: { className?: string }) {
  const { theme, setTheme } = useTheme();
  const { icon: Icon, label } = meta[theme];
  const next = order[(order.indexOf(theme) + 1) % order.length];
  return (
    <button type="button" onClick={() => setTheme(next)} title={`Theme: ${label} — click for ${meta[next].label}`}
      className={cx("inline-flex items-center gap-2 rounded-md border border-border px-2.5 py-1.5 text-small text-secondary transition hover:bg-raised hover:text-text", className)}>
      <Icon size={15} /><span className="capitalize">{label}</span>
    </button>
  );
}
