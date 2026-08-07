// Application shell (doc 30 §0): collapsible sidebar, topbar, theme toggle,
// auth guard with silent session restore.
import { useEffect, useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useAuthStore } from "../features/auth/store";
import { useLogout } from "../api/hooks";
import { Button } from "../components/ui";

const nav = [
  { to: "/", label: "Dashboard", exact: true },
  { to: "/inventory", label: "Inventory" },
  { to: "/alerts", label: "Alerts" },
  { to: "/maps", label: "Weathermaps" },
  { to: "/platform", label: "Platform" },
  { to: "/audit", label: "Audit", roles: ["admin", "auditor"] },
  { to: "/users", label: "Users", roles: ["admin"] },
  { to: "/settings", label: "Settings", roles: ["admin"] },
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

export function AppShell() {
  const user = useAuthStore((s) => s.user);
  const navigate = useNavigate();
  const logout = useLogout();
  const { dark, toggle } = useTheme();

  useEffect(() => {
    if (!user) navigate("/login", { replace: true });
  }, [user, navigate]);

  if (!user) return null;

  return (
    <div className="flex h-full">
      <aside className="flex w-52 shrink-0 flex-col border-r border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <div className="px-4 py-4 text-lg font-bold text-sky-500">NetInv</div>
        <nav className="flex flex-1 flex-col gap-0.5 px-2">
          {nav
            .filter(
              (item) =>
                !item.roles ||
                user.roles.some(
                  (r) => r === "admin" || item.roles.includes(r),
                ),
            )
            .map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.exact}
              className={({ isActive }) =>
                `rounded-md px-3 py-1.5 text-sm ${
                  isActive
                    ? "bg-sky-600/10 font-medium text-sky-600 dark:text-sky-400"
                    : "text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
                }`
              }
            >
              {item.label}
            </NavLink>
            ))}
        </nav>
        <div className="border-t border-slate-200 p-3 text-xs dark:border-slate-800">
          <div className="mb-2 flex items-center justify-between">
            <span className="font-medium">{user.display_name}</span>
            <span className="text-slate-500">{user.roles.join(", ")}</span>
          </div>
          <div className="flex gap-2">
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
          </div>
        </div>
      </aside>
      <main className="flex-1 overflow-auto p-6">
        <Outlet />
      </main>
    </div>
  );
}
