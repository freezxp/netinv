// Application shell (doc 30 §0): sidebar collapsible to icons, theme toggle,
// auth guard with silent session restore.
import { useEffect, useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useAuthStore } from "../features/auth/store";
import { useSessionRestore } from "../features/auth/useSessionRestore";
import { useLogout } from "../api/hooks";
import { Button, cx } from "../components/ui";
import { NavIcon } from "./NavIcon";

const nav = [
  { to: "/", label: "Dashboard", icon: "dashboard", exact: true },
  { to: "/inventory", label: "Inventory", icon: "inventory" },
  { to: "/alerts", label: "Alerts", icon: "alerts" },
  { to: "/maps", label: "Weathermaps", icon: "maps" },
  { to: "/platform", label: "Platform", icon: "platform" },
  { to: "/audit", label: "Audit", icon: "audit", roles: ["admin", "auditor"] },
  { to: "/users", label: "Users", icon: "users", roles: ["admin"] },
  { to: "/settings", label: "Settings", icon: "settings", roles: ["admin"] },
];

export function useTheme() {
  const [dark, setDark] = useState(
    () => localStorage.getItem("netinv-theme") !== "light",
  );
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    localStorage.setItem("netinv-theme", dark ? "dark" : "light");
  }, [dark]);
  return { dark, toggle: () => setDark((d) => !d) };
}

// Persisted like the theme: a NOC screen that reverts to a wide sidebar on
// every reload defeats the point of collapsing it.
function useSidebar() {
  const [collapsed, setCollapsed] = useState(
    () => localStorage.getItem("netinv-sidebar") === "collapsed",
  );
  useEffect(() => {
    localStorage.setItem("netinv-sidebar", collapsed ? "collapsed" : "open");
  }, [collapsed]);
  return { collapsed, toggle: () => setCollapsed((c) => !c) };
}

export function AppShell() {
  const user = useAuthStore((s) => s.user);
  const navigate = useNavigate();
  const logout = useLogout();
  const { dark, toggle } = useTheme();
  const sidebar = useSidebar();
  const restored = useSessionRestore();

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

  const collapsed = sidebar.collapsed;
  const items = nav.filter(
    (item) =>
      !item.roles ||
      user.roles.some((r) => r === "admin" || item.roles.includes(r)),
  );

  return (
    <div className="flex h-full">
      <aside
        className={cx(
          "flex shrink-0 flex-col border-r border-slate-200 bg-white transition-[width] duration-150 dark:border-slate-800 dark:bg-slate-900",
          collapsed ? "w-14" : "w-52",
        )}
      >
        <div
          className={cx(
            "flex items-center py-4",
            collapsed ? "justify-center px-0" : "justify-between px-4",
          )}
        >
          {!collapsed && (
            <span className="text-lg font-bold text-sky-500">NetInv</span>
          )}
          <button
            onClick={sidebar.toggle}
            aria-expanded={!collapsed}
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            className="rounded-md p-1 text-slate-500 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-300"
          >
            <NavIcon name={collapsed ? "expand" : "collapse"} />
          </button>
        </div>

        <nav className="flex flex-1 flex-col gap-0.5 px-2">
          {items.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.exact}
              // The label is the tooltip only while collapsed; leaving it on
              // when expanded duplicates text already on screen.
              title={collapsed ? item.label : undefined}
              className={({ isActive }) =>
                cx(
                  "flex items-center rounded-md py-1.5 text-sm",
                  collapsed ? "justify-center px-0" : "gap-2.5 px-3",
                  isActive
                    ? "bg-sky-600/10 font-medium text-sky-600 dark:text-sky-400"
                    : "text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800",
                )
              }
            >
              <NavIcon name={item.icon} />
              {/* Kept in the tree when collapsed so screen readers still get a
                  name for the link — just not painted. */}
              <span className={collapsed ? "sr-only" : undefined}>
                {item.label}
              </span>
            </NavLink>
          ))}
        </nav>

        <div
          className={cx(
            "border-t border-slate-200 text-xs dark:border-slate-800",
            collapsed ? "p-2" : "p-3",
          )}
        >
          {!collapsed && (
            <div className="mb-2 flex items-center justify-between">
              <span className="font-medium">{user.display_name}</span>
              <span className="text-slate-500">{user.roles.join(", ")}</span>
            </div>
          )}
          <div className={cx("flex gap-2", collapsed && "flex-col items-center")}>
            {collapsed ? (
              <>
                <IconButton
                  label={dark ? "Switch to light theme" : "Switch to dark theme"}
                  icon={dark ? "sun" : "moon"}
                  onClick={toggle}
                />
                <IconButton
                  label="Sign out"
                  icon="signout"
                  onClick={() => logout.mutate()}
                  disabled={logout.isPending}
                />
              </>
            ) : (
              <>
                <Button variant="ghost" onClick={toggle}>
                  {dark ? "Light" : "Dark"}
                </Button>
                <Button
                  variant="ghost"
                  onClick={() => logout.mutate()}
                  disabled={logout.isPending}
                >
                  Sign out
                </Button>
              </>
            )}
          </div>
        </div>
      </aside>
      <main className="flex-1 overflow-auto p-6">
        <Outlet />
      </main>
    </div>
  );
}

function IconButton({
  label,
  icon,
  onClick,
  disabled,
}: {
  label: string;
  icon: string;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      title={label}
      className="rounded-md p-1.5 text-slate-600 hover:bg-slate-100 disabled:opacity-50 dark:text-slate-400 dark:hover:bg-slate-800"
    >
      <NavIcon name={icon} />
    </button>
  );
}
