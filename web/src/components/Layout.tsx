import { useState } from "react";
import { NavLink, Outlet } from "react-router";
import { useAuth } from "../auth";
import { roleLabel } from "../lib/format";
import { Button, cx } from "./ui";

const nav = [
  { to: "/", label: "Dashboard", end: true },
  { to: "/scopes", label: "Scopes" },
  { to: "/reservations", label: "Reservierungen" },
  { to: "/leases", label: "Leases" },
  { to: "/policies", label: "Policies & Filter" },
  { to: "/dns", label: "DNS-Updates" },
  { to: "/ha", label: "Hochverfügbarkeit" },
  { to: "/audit", label: "Audit-Log" },
  { to: "/settings", label: "Benutzer & Einstellungen" },
];

function useTheme(): [boolean, () => void] {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  const toggle = () => {
    const next = !dark;
    document.documentElement.classList.toggle("dark", next);
    try {
      localStorage.setItem("leasorix-theme", next ? "dark" : "light");
    } catch {
      /* storage unavailable */
    }
    setDark(next);
  };
  return [dark, toggle];
}

export function Layout() {
  const { user, logout, configReadOnly, configLeader } = useAuth();
  const [open, setOpen] = useState(false);
  const [dark, toggleTheme] = useTheme();

  const links = (
    <nav className="flex flex-col gap-0.5 p-3" aria-label="Hauptnavigation">
      {nav.map((n) => (
        <NavLink
          key={n.to}
          to={n.to}
          end={n.end}
          onClick={() => setOpen(false)}
          className={({ isActive }) =>
            cx(
              "rounded-md px-3 py-2 text-sm font-medium",
              isActive
                ? "bg-teal-50 text-teal-800 dark:bg-teal-950 dark:text-teal-300"
                : "text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-slate-100",
            )
          }
        >
          {n.label}
        </NavLink>
      ))}
    </nav>
  );

  return (
    <div className="flex min-h-full">
      <aside className="hidden w-60 shrink-0 border-r border-slate-200 bg-white lg:block dark:border-slate-800 dark:bg-slate-900">
        <div className="flex h-14 items-center gap-2 border-b border-slate-200 px-5 dark:border-slate-800">
          <img src="/favicon.svg" alt="" className="h-7 w-7" />
          <span className="text-lg font-semibold tracking-tight">Leasorix</span>
        </div>
        {links}
      </aside>

      {open && (
        <div className="fixed inset-0 z-40 bg-slate-950/50 lg:hidden" onClick={() => setOpen(false)}>
          <aside className="h-full w-64 bg-white dark:bg-slate-900" onClick={(e) => e.stopPropagation()}>
            <div className="flex h-14 items-center px-5 font-semibold">Leasorix</div>
            {links}
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center justify-between gap-2 border-b border-slate-200 bg-white px-4 dark:border-slate-800 dark:bg-slate-900">
          <button
            type="button"
            className="rounded p-2 text-slate-600 hover:bg-slate-100 lg:hidden dark:text-slate-300 dark:hover:bg-slate-800"
            onClick={() => setOpen(true)}
            aria-label="Navigation öffnen"
          >
            ☰
          </button>
          <div className="flex-1" />
          <Button variant="ghost" size="sm" onClick={toggleTheme} aria-label="Farbschema wechseln" title="Hell/Dunkel">
            {dark ? "☀︎ Hell" : "☾ Dunkel"}
          </Button>
          <div className="hidden text-right text-xs sm:block">
            <div className="font-medium" data-testid="current-user">
              {user?.username}
            </div>
            <div className="text-slate-500 dark:text-slate-400">{user ? roleLabel[user.role] : ""}</div>
          </div>
          <Button size="sm" onClick={() => void logout()}>
            Abmelden
          </Button>
        </header>
        {configReadOnly && (
          <div className="border-b border-amber-200 bg-amber-50 px-4 py-2 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200" role="status" data-testid="readonly-banner">
            HA-Partner: Die Konfiguration ist auf dieser Instanz schreibgeschützt und wird vom Config-Leader{" "}
            <strong className="font-mono">{configLeader}</strong> übernommen. Änderungen bitte dort vornehmen. Leases lassen sich hier weiterhin verwalten.
          </div>
        )}
        <main className="mx-auto w-full max-w-7xl flex-1 p-4 sm:p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
