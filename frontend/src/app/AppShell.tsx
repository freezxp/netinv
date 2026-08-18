// Application shell (doc 30 §0): sidebar collapsible to icons, theme toggle,
// auth guard with silent session restore.
import { Suspense, useEffect, useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useAuthStore } from "../features/auth/store";
import { useSessionRestore } from "../features/auth/useSessionRestore";
import { useLogout } from "../api/hooks";
import { Button, cx } from "../components/ui";
import { NavIcon } from "./NavIcon";
import { Footer } from "./Footer";

const nav = [
  { to: "/", label: "Dashboard", icon: "dashboard", exact: true },
  { to: "/inventory", label: "Inventory", icon: "inventory" },
  { to: "/interfaces", label: "Interfaces", icon: "inventory" },
  { to: "/reports", label: "Reports", icon: "audit" },
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
  // On a phone the sidebar cannot hold a column of its own — at 208px it took
  // over half a 393px screen — so below md it becomes an overlay drawer that
  // is closed by default.
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

  const collapsed = sidebar.collapsed;
  // The drawer is always the full layout: an icon rail makes no sense as an
  // overlay that was just opened deliberately.
  const wide = !collapsed || drawer;
  const items = nav.filter(
    (item) =>
      !item.roles ||
      user.roles.some((r) => r === "admin" || item.roles.includes(r)),
  );

  return (
    <div className="flex h-full flex-col md:flex-row">
      {/* Phone chrome: the drawer needs a permanent way in, and the brand has
          nowhere else to live once the sidebar is off-canvas. */}
      <div className="flex items-center gap-2 border-b border-slate-200 px-3 py-2 md:hidden dark:border-slate-800">
        <button
          onClick={() => setDrawer(true)}
          aria-label="Open menu"
          aria-expanded={drawer}
          className="rounded-md p-1.5 text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
        >
          <NavIcon name="menu" className="h-5 w-5" />
        </button>
        <span className="text-base font-bold text-sky-500">NetInv</span>
      </div>

      {drawer && (
        <div
          className="fixed inset-0 z-30 bg-black/50 md:hidden"
          onClick={() => setDrawer(false)}
          aria-hidden="true"
        />
      )}

      <aside
        className={cx(
          "flex shrink-0 flex-col border-r border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900",
          // Off-canvas below md, an ordinary column from md up.
          "fixed inset-y-0 left-0 z-40 w-52 transition-transform duration-150",
          drawer ? "translate-x-0" : "-translate-x-full",
          "md:static md:z-auto md:translate-x-0 md:transition-[width]",
          collapsed ? "md:w-14" : "md:w-52",
        )}
      >
        <div
          className={cx(
            "flex items-center py-4",
            wide ? "justify-between px-4" : "justify-center px-0",
          )}
        >
          {wide && (
            <span className="text-lg font-bold text-sky-500">NetInv</span>
          )}
          {/* Collapsing is a desktop affordance; on a phone the same spot
              closes the drawer, which is what a reader expects there. */}
          <button
            onClick={() => setDrawer(false)}
            aria-label="Close menu"
            className="rounded-md p-1 text-slate-500 hover:bg-slate-100 md:hidden dark:hover:bg-slate-800"
          >
            <NavIcon name="close" />
          </button>
          <button
            onClick={sidebar.toggle}
            aria-expanded={!collapsed}
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            className="hidden rounded-md p-1 text-slate-500 hover:bg-slate-100 hover:text-slate-700 md:block dark:hover:bg-slate-800 dark:hover:text-slate-300"
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
              title={wide ? undefined : item.label}
              onClick={() => setDrawer(false)}
              className={({ isActive }) =>
                cx(
                  "flex items-center rounded-md py-1.5 text-sm",
                  wide ? "gap-2.5 px-3" : "justify-center px-0",
                  isActive
                    ? "bg-sky-600/10 font-medium text-sky-600 dark:text-sky-400"
                    : "text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800",
                )
              }
            >
              <NavIcon name={item.icon} />
              {/* Kept in the tree when collapsed so screen readers still get a
                  name for the link — just not painted. */}
              <span className={wide ? undefined : "sr-only"}>{item.label}</span>
            </NavLink>
          ))}
        </nav>

        <div
          className={cx(
            "border-t border-slate-200 text-xs dark:border-slate-800",
            wide ? "p-3" : "p-2",
          )}
        >
          {wide && (
            <div className="mb-2 flex items-center justify-between">
              <span className="font-medium">{user.display_name}</span>
              <span className="text-slate-500">{user.roles.join(", ")}</span>
            </div>
          )}
          <div className={cx("flex gap-2", !wide && "flex-col items-center")}>
            {!wide ? (
              <>
                <IconButton
                  label={
                    dark ? "Switch to light theme" : "Switch to dark theme"
                  }
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
      {/* A column so the footer sits under the content on a short page and
          after it on a long one, rather than pinned over the scroll area.
          min-w-0 on the content wrapper as well as the column, or a wide table
          inside stretches the flex item instead of scrolling. */}
      <main className="flex min-w-0 flex-1 flex-col overflow-auto p-4 md:p-6">
        <div className="min-w-0 flex-1">
          {/* Routes are code-split, so the first visit to a page waits on its
              chunk. The fallback is deliberately plain: a spinner that appears
              for 50ms on a fast connection reads as a flicker. */}
          <Suspense
            fallback={<div className="text-sm text-slate-500">Loading…</div>}
          >
            <Outlet />
          </Suspense>
        </div>
        <Footer className="mt-6 border-t border-slate-200 pt-3 dark:border-slate-800" />
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
