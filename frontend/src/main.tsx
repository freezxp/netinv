import React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider, createBrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppShell } from "./app/AppShell";
import { LoginPage } from "./features/auth/LoginPage";
import { DashboardPage } from "./features/dashboard/DashboardPage";

// Login and the dashboard are eager: one is the way in, the other is where
// every session lands, and code-splitting them would only add a round trip in
// front of first paint. Every other route is fetched when it is first opened —
// a NOC operator watching the dashboard should not be made to download the
// weathermap editor to get there. AppShell renders these inside a Suspense
// boundary.
const InventoryPage = lazyRoute(() => import("./features/inventory/InventoryPage"), "InventoryPage");
const DeviceDetailPage = lazyRoute(() => import("./features/devices/DeviceDetailPage"), "DeviceDetailPage");
const MapsListPage = lazyRoute(() => import("./features/maps/MapsListPage"), "MapsListPage");
const MapViewPage = lazyRoute(() => import("./features/maps/MapViewPage"), "MapViewPage");
const MapEditorPage = lazyRoute(() => import("./features/maps/MapEditorPage"), "MapEditorPage");
const AlertsPage = lazyRoute(() => import("./features/alerts/AlertsPage"), "AlertsPage");
const PlatformPage = lazyRoute(() => import("./features/platform/PlatformPage"), "PlatformPage");
const UsersPage = lazyRoute(() => import("./features/admin/UsersPage"), "UsersPage");
const AuditPage = lazyRoute(() => import("./features/admin/AuditPage"), "AuditPage");
const SettingsPage = lazyRoute(() => import("./features/admin/SettingsPage"), "SettingsPage");
import { ApiError } from "./api/client";
import "./styles/base.css";


// The pages are named exports; React.lazy resolves a module's default. This
// adapts one to the other without making every page file declare a default
// export purely to satisfy the router.
function lazyRoute<K extends string>(
  load: () => Promise<Record<K, React.ComponentType>>,
  name: K,
) {
  return React.lazy(async () => ({ default: (await load())[name] }));
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (count, error) =>
        // Retry transient failures only (doc 23 §6).
        count < 2 && (!(error instanceof ApiError) || error.status >= 500),
      staleTime: 5_000,
    },
  },
});

const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  {
    path: "/",
    element: <AppShell />,
    children: [
      { index: true, element: <DashboardPage /> },
      { path: "inventory", element: <InventoryPage /> },
      { path: "alerts", element: <AlertsPage /> },
      { path: "audit", element: <AuditPage /> },
      { path: "users", element: <UsersPage /> },
      { path: "settings", element: <SettingsPage /> },
      { path: "maps", element: <MapsListPage /> },
      { path: "maps/:id", element: <MapViewPage /> },
      { path: "maps/:id/edit", element: <MapEditorPage /> },
      { path: "platform", element: <PlatformPage /> },
      { path: "devices/:id", element: <DeviceDetailPage /> },
    ],
  },
]);

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);
