# 14 — Frontend Project Structure (React + TypeScript)

**Status:** draft · **Depends on:** 09 (API), 30 (UI design), ADR-009

## Stack

| Concern | Choice | Rationale |
|---|---|---|
| Build | Vite + React 18 + TypeScript strict | fast inner loop; boring |
| Routing | React Router v6 (data routers) | file-per-route convention below |
| Server state | TanStack Query | cache/invalidations map 1:1 to API resources; polling intervals for live panels |
| Client state | Zustand (small stores) | editor state, UI prefs; no Redux ceremony |
| Styling/components | Tailwind CSS + shadcn/ui (Radix) | AI-friendly, dark mode via `class` strategy, full visual control (ADR-009) |
| Charts | **uPlot** wrapped in `<TimeSeries/>` | handles NOC-wall density (25k pts) at tiny cost; one wrapper so the lib is swappable |
| Weathermap | @xyflow/react (React Flow) + custom SVG edges | pan/zoom/drag/selection solved; we own edge rendering (split-direction utilization arrows) |
| Tables | TanStack Table (virtualized) | 100k-row inventory (NFR-18) |
| Forms | react-hook-form + zod | zod schemas generated from OpenAPI |
| API client | openapi-typescript generated types + thin fetch wrapper | contract drift = compile error (doc 25 gate) |
| Tests | Vitest + Testing Library + Playwright | doc 24 |

## Layout — feature-sliced

```
frontend/
├── src/
│   ├── app/                    # shell: providers, router, layout, theme, error boundary
│   │   ├── routes.tsx          # route table → features (doc 30 sitemap)
│   │   └── AppShell.tsx        # sidebar nav + topbar + <Outlet/>
│   ├── api/                    # generated types + client + TanStack Query hooks
│   │   ├── schema.d.ts         #   openapi-typescript output (generated, committed)
│   │   ├── client.ts           #   fetch wrapper: auth header, refresh-on-401, error envelope
│   │   └── hooks/              #   useDevices(), useAlerts(), useMapLive(id)…
│   ├── features/               # one folder per product area = doc 30 page
│   │   ├── auth/  dashboard/  inventory/  device-detail/  weathermap/
│   │   ├── alerts/  audit/  settings/  users/  platform/   # sites·pollers·connectors·credentials
│   │   └── <feature>/          #   components/ hooks/ store.ts index.ts (public surface)
│   ├── components/             # shared: ui/ (shadcn), TimeSeries, StatusBadge,
│   │                           # SeverityPill, DataTable, FilterBar, ExportMenu, EmptyState
│   ├── lib/                    # formatters (bps, %, dBm, uptime), filter-grammar codec,
│   │                           # ws/polling helpers, permissions.ts (RBAC gates)
│   └── styles/                 # tailwind.css, theme tokens (doc 30 §theme)
├── e2e/                        # Playwright specs (doc 24)
├── index.html  vite.config.ts  tsconfig.json  package.json
```

Rules: features may import `components/`, `lib/`, `api/` — never each other (eslint boundary rule; cross-feature needs promote to shared). All server data through `api/hooks` (no raw fetch in features). All permission-gated UI through `<Can permission="alerts:ack">` helper so RBAC is greppable.

## Key implementation notes

- **Live data:** TanStack Query `refetchInterval`: dashboard panels 30 s, map live 15–30 s, alert list 15 s — all hitting cached aggregate endpoints (doc 05 §7), so polling is cheap. SSE upgrade is a v1.x seam (`api/client.ts` isolates it).
- **Weathermap editor:** React Flow nodes = custom components (device node with status ring, site node, label). Edges = custom SVG with two half-arrows (A→B, B→A) colored via the classic scale from `options.util_scale`; editor state in a Zustand store with command-pattern undo/redo (FR-MAP-02); autosave debounced 2 s to `PUT /maps/{id}/draft`.
- **Theme:** CSS variables + Tailwind `dark:` class; user preference from `/users/me/preferences`, `system` honors `prefers-color-scheme` (FR-SET-03). Chart & map colors read the same tokens so themes stay consistent (doc 30).
- **Auth flow:** access JWT in memory only (never localStorage); silent refresh via httpOnly cookie on 401 (doc 07 §4); idle logout on refresh expiry.
- **URL as state:** inventory filters/sort/cursor serialize to the query string (FR-DEV-04) — shareable and AI-testable.
- **Perf budget:** initial JS ≤ 350 kB gzip (uPlot ~40 kB, React Flow lazy-loaded on map routes); route-level code splitting; Lighthouse CI gate in doc 25.
