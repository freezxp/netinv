import React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider, createBrowserRouter } from "react-router-dom";
import { AppShell } from "./app/AppShell";
import "./styles/base.css";

const router = createBrowserRouter([
  {
    path: "/",
    element: <AppShell />,
    children: [
      // Feature routes land here sprint by sprint (doc 30 sitemap).
    ],
  },
]);

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>,
);
