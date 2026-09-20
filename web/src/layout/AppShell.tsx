import { clsx } from "clsx";
import { useTranslation } from "react-i18next";
import { Boxes, Container, LayoutDashboard, Languages, LogOut, Moon, Settings, Sun, Monitor, Menu, X } from "lucide-react";
import { useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { useAuth } from "@/features/auth/AuthContext";
import { Logo } from "./Logo";
import { useTheme, type Theme } from "./theme";
import { useDashboard } from "@/api/hooks";
import { Button } from "@/components/ui";
import { currentLanguage, languages, setLanguage } from "@/i18n";

const nav = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard, end: true },
  { to: "/projects", label: "Projects", icon: Boxes },
  { to: "/docker", label: "Docker", icon: Container },
  { to: "/settings", label: "Settings", icon: Settings },
];

const themeOrder: Theme[] = ["system", "light", "dark"];

export function AppShell() {
  const { t } = useTranslation();
  const auth = useAuth();
  const [theme, setTheme] = useTheme();
  const [open, setOpen] = useState(false);
  const dashboard = useDashboard();
  const dockerDown = dashboard.data ? !dashboard.data.docker.connected : false;

  const ThemeIcon = theme === "dark" ? Moon : theme === "light" ? Sun : Monitor;
  const cycleTheme = () => setTheme(themeOrder[(themeOrder.indexOf(theme) + 1) % themeOrder.length] ?? "system");

  const sidebar = (
    <nav className="flex h-full flex-col" aria-label={t("Main navigation")}>
      <div className="flex h-14 items-center justify-between px-4">
        <Logo withText />
        <button className="rounded-md p-1 text-muted hover:bg-muted lg:hidden" onClick={() => setOpen(false)} aria-label={t("Close menu")}>
          <X className="size-5" />
        </button>
      </div>
      <ul className="flex-1 space-y-0.5 px-2 py-2">
        {nav.map((item) => (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end={item.end ?? false}
              onClick={() => setOpen(false)}
              className={({ isActive }) =>
                clsx(
                  "flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm font-medium transition-colors",
                  isActive ? "bg-accent-500/10 text-accent-600 dark:text-accent-300" : "text-muted hover:bg-muted hover:text-fg",
                )
              }
            >
              <item.icon className="size-4" aria-hidden />
              {t(item.label)}
            </NavLink>
          </li>
        ))}
      </ul>
      <div className="border-t border-default p-3">
        <div className="flex items-center justify-between gap-2">
          <div className="min-w-0">
            <p className="truncate text-sm font-medium text-fg">{auth.user?.username}</p>
            <p className="truncate text-xs text-subtle">{dashboard.data?.version ? `Envoryx ${dashboard.data.version}` : "Envoryx"}</p>
          </div>
          <div className="flex items-center gap-1">
            <LanguageButton />
            <Button variant="ghost" size="sm" onClick={cycleTheme} aria-label={t("Theme: {{theme}}", { theme })} title={t("Theme: {{theme}}", { theme })}>
              <ThemeIcon className="size-4" />
            </Button>
            <Button variant="ghost" size="sm" onClick={() => void auth.logout()} aria-label={t("Sign out")} title={t("Sign out")}>
              <LogOut className="size-4" />
            </Button>
          </div>
        </div>
      </div>
    </nav>
  );

  return (
    <div className="flex min-h-full bg-app">
      <aside className="hidden w-60 shrink-0 border-r border-default bg-elevated lg:block">{sidebar}</aside>
      {open && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-black/50" onClick={() => setOpen(false)} aria-hidden />
          <aside className="absolute inset-y-0 left-0 w-64 border-r border-default bg-elevated shadow-xl">{sidebar}</aside>
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center gap-3 border-b border-default bg-elevated px-4 lg:hidden">
          <button className="rounded-md p-1 text-muted hover:bg-muted" onClick={() => setOpen(true)} aria-label={t("Open menu")}>
            <Menu className="size-5" />
          </button>
          <Logo withText />
        </header>
        {dockerDown && (
          <div className="border-b border-red-500/30 bg-red-500/10 px-4 py-2 text-sm text-red-600 dark:text-red-400" role="alert">
            {t("Docker engine is not reachable. Project operations are unavailable until the connection is restored.")}
          </div>
        )}
        <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 sm:px-6 lg:px-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

/** Language selector listing every available UI language. */
function LanguageButton() {
  const { t } = useTranslation();
  const current = currentLanguage();
  return (
    <label className="relative inline-flex items-center" title={t("Language")}>
      <Languages className="pointer-events-none absolute left-2 size-4 text-muted" aria-hidden />
      <select
        aria-label={t("Language")}
        value={current}
        onChange={(e) => void setLanguage(e.target.value)}
        className="h-8 appearance-none rounded-md bg-transparent pl-8 pr-2 text-xs text-muted hover:bg-muted hover:text-fg focus:outline-none"
      >
        {Object.entries(languages).map(([code, name]) => (
          <option key={code} value={code}>{name}</option>
        ))}
      </select>
    </label>
  );
}
