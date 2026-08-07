import React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider, createBrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppShell } from "./app/AppShell";
import { LoginPage } from "./features/auth/LoginPage";
import { InventoryPage } from "./features/inventory/InventoryPage";
import { DashboardPage } from "./features/dashboard/DashboardPage";
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

function Placeholder({ title }: { title: string }) {
  return (
    <div className="text-slate-500">
      <h1 className="mb-2 text-xl font-semibold text-slate-900 dark:text-slate-200">
        {title}
      </h1>
      Arrives in an upcoming sprint (docs/27-sprint-planning.md).
    </div>
  );
}

const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  {
    path: "/",
    element: <AppShell />,
    children: [
      { index: true, element: <DashboardPage /> },
      { path: "inventory", element: <InventoryPage /> },
      { path: "alerts", element: <Placeholder title="Alerts" /> },
      { path: "maps", element: <Placeholder title="Weathermaps" /> },
      { path: "platform", element: <Placeholder title="Platform" /> },
      { path: "devices/:id", element: <Placeholder title="Device detail" /> },
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
