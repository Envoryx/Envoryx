import { useCallback, useEffect, useState } from "react";

export type Theme = "light" | "dark" | "system";
const KEY = "envoryx.theme";

function apply(theme: Theme) {
  const dark = theme === "dark" || (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
}

export function useTheme(): [Theme, (t: Theme) => void] {
  const [theme, setThemeState] = useState<Theme>(() => {
    try {
      const v = localStorage.getItem(KEY);
      return v === "light" || v === "dark" ? v : "system";
    } catch {
      return "system";
    }
  });

  useEffect(() => {
    apply(theme);
    if (theme !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const handler = () => apply("system");
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, [theme]);

  const setTheme = useCallback((t: Theme) => {
    setThemeState(t);
    try {
      if (t === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, t);
    } catch {
      /* storage unavailable */
    }
  }, []);

  return [theme, setTheme];
}

export const accents = ["mint", "ocean", "violet", "amber", "rose"] as const;
export type Accent = (typeof accents)[number];
const ACCENT_KEY = "envoryx.accent";

function isAccent(v: unknown): v is Accent {
  return accents.includes(v as Accent);
}

function applyAccent(accent: Accent) {
  if (accent === "mint") document.documentElement.removeAttribute("data-accent");
  else document.documentElement.dataset.accent = accent;
}

/** Accent colour of the UI and logo; mint is the brand default. Stored per browser like the theme. */
export function useAccent(): [Accent, (a: Accent) => void] {
  const [accent, setAccentState] = useState<Accent>(() => {
    try {
      const v = localStorage.getItem(ACCENT_KEY);
      return isAccent(v) ? v : "mint";
    } catch {
      return "mint";
    }
  });

  useEffect(() => applyAccent(accent), [accent]);

  const setAccent = useCallback((a: Accent) => {
    setAccentState(a);
    try {
      if (a === "mint") localStorage.removeItem(ACCENT_KEY);
      else localStorage.setItem(ACCENT_KEY, a);
    } catch {
      /* storage unavailable */
    }
  }, []);

  return [accent, setAccent];
}
