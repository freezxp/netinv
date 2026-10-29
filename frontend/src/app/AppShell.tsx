// Application shell (doc 30 §0), laid out like PICA's portal: a
// full-height sidebar grouped into collapsible sections that folds to an icon
// rail, a 52px top bar carrying the account menu, and a centred page. Auth
// guard with silent session restore.
import { Suspense, useEffect, useRef, useState } from "react";
import { Link, NavLink, Outlet, useNavigate } from "react-router-dom";
import { useAuthStore } from "../features/auth/store";
import { useSessionRestore } from "../features/auth/useSessionRestore";
import { useLogout } from "../api/hooks";
import { cx } from "../components/ui";
import { NavIcon } from "./NavIcon";
import { Footer } from "./Footer";
import {
  applyStyle,
  applyTheme,
  storedStyle,
  storedTheme,
  type Style,
  type ThemeMode,
} from "./appearance";

interface NavItem {
  to: string;
  label: string;
  icon: string;
  exact?: boolean;
  roles?: string[];
}

// Grouped by job, as PICA's sidebar is. The first group has no heading.
const nav: { section: string; items: NavItem[] }[] = [
  { section: "", items: [{ to: "/", label: "Dashboard", icon: "dashboard", exact: true }] },
  {
    section: "Monitoring",
    items: [
      { to: "/inventory", label: "Inventory", icon: "inventory" },
      { to: "/interfaces", label: "Interfaces", icon: "ports" },
      { to: "/flows", label: "Flows", icon: "flows" },
      { to: "/maps", label: "Weathermaps", icon: "maps" },
      { to: "/alerts", label: "Alerts", icon: "alerts" },
      { to: "/traps", label: "Traps", icon: "traps" },
    ],
  },
  {
    section: "Reporting",
    items: [{ to: "/reports", label: "Reports", icon: "reports" }],
  },
  {
    section: "Administration",
    items: [
      { to: "/platform", label: "Platform", icon: "platform" },
      { to: "/users", label: "Users", icon: "users", roles: ["admin"] },
      { to: "/audit", label: "Audit log", icon: "audit", roles: ["admin", "auditor"] },
      { to: "/settings", label: "Settings", icon: "settings", roles: ["admin"] },
    ],
  },
];

// Remembered per browser; blocked storage just falls back to the default.
function useStored<T>(key: string, fallback: T) {
  const [v, setV] = useState<T>(() => {
    try {
      const raw = localStorage.getItem(key);
      return raw === null ? fallback : (JSON.parse(raw) as T);
    } catch {
      return fallback;
    }
  });
  const set = (next: T) => {
    setV(next);
    try {
      localStorage.setItem(key, JSON.stringify(next));
    } catch {
      /* ignore */
    }
  };
  return [v, set] as const;
}

const THEME_NEXT: Record<ThemeMode, ThemeMode> = {
  light: "system",
  system: "dark",
  dark: "light",
};

export function useTheme() {
  const [mode, setMode] = useState<ThemeMode>(storedTheme);
  return {
    mode,
    cycle: () => {
      const next = THEME_NEXT[mode];
      applyTheme(next);
      setMode(next);
    },
  };
}

export function useStyle() {
  const [style, setStyle] = useState<Style>(storedStyle);
  return {
    style,
    toggle: () => {
      const next = style === "modern" ? "legacy" : "modern";
      applyStyle(next);
      setStyle(next);
    },
  };
}

export function AppShell() {
  const user = useAuthStore((s) => s.user);
  const navigate = useNavigate();
  const logout = useLogout();
  const theme = useTheme();
  const style = useStyle();
  const restored = useSessionRestore();
  // A NOC screen that reverts to a wide sidebar on every reload defeats the
  // point of collapsing it, so both the rail and the folded sections persist.
  const [collapsed, setCollapsed] = useStored("netinv-sidebar-rail", false);
  const [closed, setClosed] = useStored<string[]>("netinv-sidebar-sections", []);
  // Below md the sidebar is an overlay drawer, closed by default — a column
  // of its own would take most of a phone screen.
  const [drawer, setDrawer] = useState(false);

  useEffect(() => {
    if (restored && !user) navigate("/login", { replace: true });
  }, [restored, user, navigate]);

  if (!restored) {
    return (
      <div className="flex h-full items-center justify-center text-slate-500">
        Restoring session…
      </div>
    );
  }
  if (!user) return null;

  // The drawer is always the full layout: an icon rail makes no sense as an
  // overlay that was just opened deliberately.
  const rail = collapsed && !drawer;
  const visible = (i: NavItem) =>
    !i.roles || user.roles.some((r) => r === "admin" || i.roles!.includes(r));
  const groups = nav
    .map((g) => ({ ...g, items: g.items.filter(visible) }))
    .filter((g) => g.items.length);
  const toggleSection = (s: string) =>
    setClosed(closed.includes(s) ? closed.filter((x) => x !== s) : [...closed, s]);

  return (
    <div className="flex h-full">
      {drawer && (
        <div
          className="fixed inset-0 z-30 bg-black/50 md:hidden"
          onClick={() => setDrawer(false)}
          aria-hidden="true"
        />
      )}

      <aside
        aria-label="Main navigation"
        className={cx(
          "ni-shell flex shrink-0 flex-col border-r border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900",
          "fixed inset-y-0 left-0 z-40 w-[232px] transition-transform duration-150",
          drawer ? "translate-x-0" : "-translate-x-full",
          "md:static md:z-auto md:translate-x-0 md:transition-[width]",
          collapsed ? "md:w-14" : "md:w-[232px]",
        )}
      >
        <div
          className={cx(
            "flex py-[18px]",
            rail ? "flex-col items-center gap-2" : "items-start justify-between px-5",
          )}
        >
          <Link to="/" onClick={() => setDrawer(false)} className="min-w-0">
            {rail ? (
              <span className="font-mono text-sm font-semibold">ni</span>
            ) : (
              <>
                <div className="font-mono text-sm font-semibold leading-tight tracking-[-0.01em] text-slate-950 dark:text-slate-50">
                  NetInv
                </div>
                <div className="font-mono text-[10px] uppercase tracking-[0.14em] text-sky-600 dark:text-sky-400">
                  network monitoring
                </div>
              </>
            )}
          </Link>
          <button
            onClick={() => setCollapsed(!collapsed)}
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            className="hidden px-1.5 text-base leading-none text-slate-500 hover:text-slate-700 md:block dark:text-slate-400 dark:hover:text-slate-200"
          >
            {collapsed ? "»" : "«"}
          </button>
          <button
            onClick={() => setDrawer(false)}
            aria-label="Close menu"
            className="rounded-sm p-1 text-slate-500 md:hidden"
          >
            <NavIcon name="close" />
          </button>
        </div>

        <nav className={cx("flex-1 overflow-y-auto pb-4", rail ? "px-1" : "px-2")}>
          {groups.map((g) => {
            const folded = closed.includes(g.section);
            return (
              <div key={g.section || "home"} className="mb-4">
                {g.section && !rail && (
                  <button
                    onClick={() => toggleSection(g.section)}
                    aria-expanded={!folded}
                    className="eyebrow flex w-full items-center gap-1 px-3 pb-1 text-left"
                  >
                    <span
                      aria-hidden
                      className={cx(
                        "inline-block w-2 text-[8px] transition-transform",
                        !folded && "rotate-90",
                      )}
                    >
                      ▶
                    </span>
                    {g.section}
                  </button>
                )}
                {/* In the rail every item shows: there is no heading to fold
                    it under. */}
                {(rail || !folded) &&
                  g.items.map((item) => (
                    <NavLink
                      key={item.to}
                      to={item.to}
                      end={item.exact}
                      title={rail ? item.label : undefined}
                      onClick={() => setDrawer(false)}
                      className={({ isActive }) =>
                        cx(
                          "group flex items-center gap-2 rounded-sm text-sm",
                          rail ? "justify-center py-2" : "px-3 py-1.5",
                          isActive
                            ? "bg-sky-50 font-medium text-sky-700 dark:bg-sky-950 dark:text-sky-300"
                            : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
                        )
                      }
                    >
                      {({ isActive }) => (
                        <>
                          <span
                            className={cx(
                              "inline-flex w-5 justify-center",
                              isActive
                                ? "text-sky-700 dark:text-sky-300"
                                : "text-slate-500 dark:text-slate-400",
                            )}
                          >
                            <NavIcon name={item.icon} className="h-4 w-4" />
                          </span>
                          {/* Kept in the tree in the rail so screen readers
                              still get a name for the link. */}
                          <span className={rail ? "sr-only" : "truncate"}>{item.label}</span>
                        </>
                      )}
                    </NavLink>
                  ))}
              </div>
            );
          })}
        </nav>

        <div
          className={cx(
            "border-t border-slate-200 py-2.5 font-mono text-[10px] tracking-[0.12em] text-slate-500 dark:border-slate-800 dark:text-slate-400",
            rail ? "text-center" : "px-5",
          )}
          title={`NetInv ${__APP_VERSION__}`}
        >
          {rail ? __APP_VERSION__.replace(/^v/, "").split(".").slice(0, 2).join(".") : `NetInv ${__APP_VERSION__}`}
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="ni-shell flex h-[52px] shrink-0 items-center justify-between border-b border-slate-200 bg-white px-4 md:px-9 dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center gap-2">
            <button
              onClick={() => setDrawer(true)}
              aria-label="Open menu"
              aria-expanded={drawer}
              className="rounded-sm p-1.5 text-slate-600 hover:bg-slate-100 md:hidden dark:text-slate-300 dark:hover:bg-slate-800"
            >
              <NavIcon name="menu" className="h-5 w-5" />
            </button>
            <span className="font-mono text-sm font-semibold md:hidden">NetInv</span>
          </div>
          <UserMenu
            name={user.display_name}
            roles={user.roles.join(", ")}
            theme={theme.mode}
            onTheme={theme.cycle}
            appearance={style.style}
            onAppearance={style.toggle}
            onLogout={() => logout.mutate()}
            loggingOut={logout.isPending}
          />
        </header>

        {/* A column so the footer sits under the content on a short page and
            after it on a long one. min-w-0 on both, or a wide table inside
            stretches the flex item instead of scrolling. */}
        <main className="flex min-w-0 flex-1 flex-col overflow-auto">
          <div className="page mx-auto flex w-full min-w-0 max-w-[1400px] flex-1 flex-col">
            <div className="min-w-0 flex-1">
              {/* Routes are code-split; the fallback is deliberately plain, as
                  a spinner shown for 50ms reads as a flicker. */}
              <Suspense fallback={<div className="text-sm text-slate-500">Loading…</div>}>
                <Outlet />
              </Suspense>
            </div>
            <Footer className="mt-6 border-t border-slate-200 pt-3 dark:border-slate-800" />
          </div>
        </main>
      </div>
    </div>
  );
}

// Account menu (top right): initials, name and roles; opens the colour theme,
// the appearance (Legacy or Modern) and sign-out.
function UserMenu({
  name,
  roles,
  theme,
  onTheme,
  appearance,
  onAppearance,
  onLogout,
  loggingOut,
}: {
  name: string;
  roles: string;
  theme: ThemeMode;
  onTheme: () => void;
  appearance: Style;
  onAppearance: () => void;
  onLogout: () => void;
  loggingOut: boolean;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === "Escape" : !ref.current?.contains(e.target as Node))
        setOpen(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", close);
    };
  }, [open]);
  const initials =
    name
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0]!.toUpperCase())
      .join("") || "?";

  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen(!open)}
        aria-label="User menu"
        aria-haspopup="menu"
        aria-expanded={open}
        className={cx(
          "flex items-center gap-2 rounded-lg px-1.5 py-1 hover:bg-slate-100 dark:hover:bg-slate-800",
          open && "bg-slate-100 dark:bg-slate-800",
        )}
      >
        <span className="flex h-[30px] w-[30px] items-center justify-center rounded-full bg-sky-600 text-xs font-semibold text-white dark:bg-sky-400 dark:text-slate-950">
          {initials}
        </span>
        <span className="hidden max-w-[180px] text-left sm:block">
          <span className="block truncate text-xs font-semibold">{name}</span>
          <span className="block truncate font-mono text-[10px] text-slate-500 dark:text-slate-400">
            {roles}
          </span>
        </span>
        <NavIcon name="chevron" className="h-3.5 w-3.5 text-slate-500" />
      </button>
      {open && (
        <div
          role="menu"
          className="ni-floating absolute right-0 z-50 mt-1 w-[260px] rounded-sm border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-700 dark:bg-slate-900"
        >
          <div className="px-3 py-2">
            <div className="truncate text-sm font-semibold">{name}</div>
            <div className="truncate text-xs text-slate-500 dark:text-slate-400">{roles}</div>
          </div>
          <div className="my-1 border-t border-slate-200 dark:border-slate-700" />
          <MenuItem icon="palette" onClick={onTheme}>
            Theme: {theme}
          </MenuItem>
          <MenuItem icon="palette" onClick={onAppearance}>
            Appearance: {appearance === "modern" ? "Modern" : "Legacy"}
          </MenuItem>
          <div className="my-1 border-t border-slate-200 dark:border-slate-700" />
          <MenuItem icon="signout" onClick={onLogout} disabled={loggingOut}>
            Sign out
          </MenuItem>
        </div>
      )}
    </div>
  );
}

function MenuItem({
  icon,
  children,
  onClick,
  disabled,
}: {
  icon: string;
  children: React.ReactNode;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <button
      role="menuitem"
      onClick={onClick}
      disabled={disabled}
      className="flex w-full items-center gap-2.5 px-3 py-1.5 text-left text-sm hover:bg-slate-100 disabled:opacity-50 dark:hover:bg-slate-800"
    >
      <NavIcon name={icon} className="h-3.5 w-3.5 text-slate-500" />
      {children}
    </button>
  );
}
