import React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider, createBrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppShell } from "./app/AppShell";
import { LoginPage } from "./features/auth/LoginPage";
import { InventoryPage } from "./features/inventory/InventoryPage";
import { DashboardPage } from "./features/dashboard/DashboardPage";
import { DeviceDetailPage } from "./features/devices/DeviceDetailPage";
import { MapsListPage } from "./features/maps/MapsListPage";
import { MapViewPage } from "./features/maps/MapViewPage";
import { MapEditorPage } from "./features/maps/MapEditorPage";
import { AlertsPage } from "./features/alerts/AlertsPage";
import { PlatformPage } from "./features/platform/PlatformPage";
import { UsersPage } from "./features/admin/UsersPage";
import { AuditPage } from "./features/admin/AuditPage";
import { SettingsPage } from "./features/admin/SettingsPage";
import { ApiError } from "./api/client";
import "./styles/base.css";

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
