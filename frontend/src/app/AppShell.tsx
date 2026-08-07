import { Outlet } from "react-router-dom";

// Sprint-1 placeholder shell. Sprint 11 replaces this with the sidebar/topbar
// layout specified in doc 30 §0.
export function AppShell() {
  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">NetInv</div>
        <nav>
          <span className="navitem">Dashboard</span>
          <span className="navitem">Weathermaps</span>
          <span className="navitem">Inventory</span>
          <span className="navitem">Alerts</span>
        </nav>
      </aside>
      <main className="content">
        <h1>NetInv</h1>
        <p>
          Build phase — Sprint 1 shell. Backend services and features arrive per{" "}
          <code>docs/27-sprint-planning.md</code>.
        </p>
        <Outlet />
      </main>
    </div>
  );
}
