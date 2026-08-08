# 30 — UI Design (every page)

**Status:** draft · **Depends on:** 02, 09, 14 · Components/stack: doc 14

The kickoff brief listed pages from a virtualization-flavored template; mapped to this product: *Asset Detail / Host Detail* → **Device Detail** (one page; future host monitoring reuses it), *Storage* → deferred with category 8/10 (doc 29), *Network* → **Topology & Weathermaps**. All listed concerns are covered below.

## 0. Shell, navigation & design language

- **Shell:** left sidebar (collapsible to icons): Dashboard · Weathermaps · Inventory · Alerts · Topology · Platform · Audit · Settings · Users — items RBAC-filtered (`<Can>`, doc 14). Topbar: global search (⌘K — devices/interfaces/maps/pages), site filter (persistent, scopes every page), alert bell with severity badge, user menu (profile, theme, logout).
- **Design language:** dense-but-calm ops UI. Tokens: 4 px spacing grid; `Inter` UI font, `JetBrains Mono` for IPs/serials/OIDs. **Status colors are the product's grammar** and identical everywhere (badges, map nodes, heatmap, charts): up/ok `green-500`, warning `amber-500`, critical/down `red-500`, unreachable `slate-400`, disabled/unknown `slate-600 outline`, info `sky-500`. Severity always paired with an icon/shape (color-blind safe); WCAG AA contrast in both themes.
- **Dark mode** (default for NOC): CSS-variable tokens flip via `class="dark"`; charts and weathermap consume the same variables (no hardcoded chart palettes); user preference `system|light|dark` per FR-SET-03. Light theme for office/report use.
- **Responsive** (NFR-60): breakpoints ≥1280 full grid · 768–1280 dashboard collapses to 2-col, tables shed secondary columns · <768 the sidebar leaves the layout entirely and becomes an overlay drawer behind a topbar hamburger — at 208px wide it otherwise took over half a 393px screen — closing on navigation and on backdrop tap; data tables keep a min-width and scroll inside their own card rather than squeezing columns to illegibility; dialogs size to the viewport instead of a fixed width; and the view is read-mostly (status row, alert list, ack actions; weathermap view-only pan/zoom; editor desktop-only). NOC-wall mode: `/wall` route — dashboard variant with no chrome, larger type, auto-cycling optional.
- **Global patterns:** every list = FilterBar (chips + saved filters + URL state) + virtualized DataTable (sortable, column picker, export menu) + row → detail drawer or page. Every timestamp shows relative + absolute on hover, user-timezone display, UTC storage. Every panel shows data-age when stale (doc 23 §6). Empty states teach ("No devices yet — Add device or Import CSV"). Destructive actions: typed-confirmation modal. All data via TanStack Query hooks with skeleton loaders; errors per doc 23 §6.

## 1. Auth pages

`/login`: centered card, product mark, username/password, lockout + generic-failure messaging (no user enumeration), forced password-change flow (seeded admin). `/logout` interstitial. No self-registration (Admin creates users). Session-expired toast → redirect preserving return URL.

## 2. Dashboard `/` (the NOC wall — PRD §4.2)

12-col grid, panels in fixed order (v1: layout fixed, per-user layout is post-v1):

| Row | Panels |
|---|---|
| 1 (full width) | **Status summary strip:** device counts up/down/unreachable (click → filtered inventory), alert counts by severity (click → alerts), availability-24h %, aggregate throughput now (in/out) with 1h sparkline |
| 2 (⅔ + ⅓) | **Active alerts** (severity→recency sort, colored left-border rows: severity pill, device, metric, value vs threshold, duration, ack avatar; row click → deep-link graph; inline ack button) · **Device health heatmap** (one cell per device, worst-metric color, site-grouped; hover card: name + top metric; click → device) |
| 3 (⅓×3) | **Top-N tabs:** interface utilization % · CPU/memory · errors+discards (the diagnostic list) — each row: rank, device/interface, sparkline, value, trend arrow |
| 4 (½+½) | **Aggregate WAN/uplink bandwidth** (stacked in/out, 24 h, uPlot) · **Latency & loss to key targets** (multi-line + loss band) |
| 5 (⅔+⅓) | **Embedded weathermap** (user-selected default map, view-only, live) · **Capacity watchlist:** links >70% sustained with utilization bar, trend arrow, days-to-90% estimate |

Recent events stream (P1) docks as row 6 when enabled. All panels read cached aggregates (doc 05 §7); refresh 30 s; panel headers show as-of time.

## 3. Weathermaps `/maps`, `/maps/:id`, `/maps/:id/edit` (flagship)

- **List:** card grid with live mini-thumbnails, name, node/link counts, worst-state ring, updated-by/when; New Map / Import JSON.
- **Viewer:** full-bleed canvas; pan/zoom (wheel/pinch), fit/100% controls; legend (utilization scale + node states) collapsible; link hover → tooltip (both directions bps, %, errors, speed) with 1h mini-sparkline; link click → interface detail; node click → device peek drawer (status, top alerts, CPU/mem) with "open device". Auto-refresh ≤30 s (FR-MAP-05); presence-aware (doc 05 §7). Fullscreen/wall mode.
- **Editor** (Operator+): left palette (Device node — search picker w/ status preview; Site node; Cloud node; Text label), canvas with snap-grid + alignment guides, right inspector (selected node/link properties: label mode, link endpoints A/B pickers as device→interface cascading selects, bandwidth override, width, curve style). Toolbar: undo/redo, draft-saved indicator (autosave 2 s), **Suggestions** panel (LLDP adjacencies not yet drawn → one-click add, FR-MAP-06), validate (broken bindings listed), Publish (diff summary vs published), Export JSON. Unsaved-draft banner on the viewer if a newer draft exists.
- Link rendering spec: each link = two half-arrows (A→B, B→A) colored independently by that direction's utilization on the classic Cacti scale (options.util_scale); grey dashed = endpoint missing/removed (doc 11 §4); black = no data.

## 4. Inventory `/inventory` (+ groups)

FilterBar: search (name/IP/serial/model — FR-DEV-03), facets vendor/site/status/group/firmware/tag; grouping selector (by site → collapsible sections). Table columns (picker-managed): status dot, **name — the device's own sysName leads, with the operator's label kept beside it when the two differ (neither is lost; sync never overwrites operator-owned fields, doc 11 §3)**, mgmt IP, site, model, **live CPU / memory / hottest-sensor temperature** (one shared `/dashboard/device-health` payload for the page, threshold-tinted, dash when the connector exposes no health), tags. Serial, OS, interface counts and last-sync are available via the column picker. Each row carries a **Retire** action (soft: stops polling, hides from the default list, keeps history, restorable); filtering by status `retired` reveals retired devices, where an Admin gets **Restore** and **Delete** — the latter behind a typed-name confirmation that states exactly what is destroyed (interfaces, schedules, topology links, change history) and what is not (metrics expire by retention; audit records persist). Each row also opens an **OID browser** — a live SNMP walk of the device with type-aware rendering (binary as hex, IPs and TimeTicks decoded), preset roots (mib-2, interfaces, host-resources, UCD-SNMP, LLDP, vendor tree), free-text filter and copy-all. It answers both "what does this platform actually support?" when writing a connector and "why is this metric empty?" in the field. Bulk-select → assign group/profile, enable/disable, export. Export menu: CSV/XLSX now (PDF P1) — exports the filter result, async job with toast → download (doc 09 §13). Buttons: Add Device (modal wizard: identity → site/connector/credential [test button inline] → profile → confirm), Import CSV (upload → column mapping → per-row validation report → commit). Device Groups tab: group CRUD, membership management. Interfaces tab: cross-device interface search ("find alias 'uplink'").

## 5. Device Detail `/devices/:id` (asset + host detail)

Header: status dot + name + IP + vendor/model chips, site, uptime, active-alert pills; actions: Sync now, Enable/Disable, Edit, Retire (Admin: Purge). Tabs:

- **Overview:** identity card (sysName/Descr/Location/Contact, serial, firmware, connector+version, credential name, profile) · health tiles (CPU, memory, worst temp, fan/PSU status, poll success 24h) each with 24 h sparkline · mini-topology (this device + LLDP neighbors, click-through) · active alerts list.
- **Interfaces:** table (status, name, alias, speed, utilization in/out bars, errors/discards 24h, optic dBm where present, monitor toggle); row expand → inline 24 h traffic graph; row click → **Interface detail** `/devices/:id/interfaces/:ifId`: full graph set (traffic, packets by type, errors/discards, utilization, optics) with range picker, link partner (LLDP), maps-containing-this-interface list.
- **Health:** stat tiles (CPU %, memory %, 1-minute load, hottest sensor, memory used/total) with warn/critical tinting, over a graph set: CPU utilization, memory utilization, temperature per named sensor, and load average (1/5/15m) — all 24 h. Sources are connector-dependent (vendor MIBs on Cisco/Juniper/Huawei; UCD-SNMP + LM-SENSORS on net-snmp devices such as Ubiquiti UniFi gateways), so the tab states plainly when a device's connector exposes no health rather than rendering empty charts. Fan RPM, PSU state timeline, optics per port and the component inventory table extend this tab as those collectors land.
- **Availability:** RTT min/avg/max + jitter + loss graphs, availability % tiles (24h/7d/30d), poll success/timeout timeline.
- **History:** asset-history timeline (FR-DEV-07) — grouped by sync run: field chips old→new, hardware-replaced highlighted; filter by object/field; link to the sync run.
- **Alerts:** instance history for this device with state timelines.
- **Sync:** sync-run list (trigger, duration, outcome, changes), on-demand sync button, next scheduled.

## 6. Alerts `/alerts`

Tabs: Active (default: firing+acked, severity→recency), History, Silences, Rules.
- **Active:** table with severity border, state chip (firing pulse / acked with avatar+comment tooltip / flapping badge), device/interface, value vs threshold, duration, deliveries status icons; row click → detail drawer: description, graph embed (the explaining graph, PRD G3), event timeline, delivery log, ack form, silence-from-alert shortcut.
- **Rules** (Operator+): table (name, kind, severity, scope summary, enabled, builtin lock icon); editor: kind picker → threshold form (metric picker with catalog autocomplete, operator, value, for-duration, scope builder with live match preview via `/preview`) | state form | inventory form; severity, annotations (message template with variable hints, runbook URL); test-evaluate button.
- **Silences:** active/expired lists, create (scope builder, duration, mandatory reason), revoke.

## 7. Topology `/topology`

Auto-generated LLDP/CDP graph (read-only; force layout with site clustering): filter by site, highlight unmanaged neighbors ("ghost" nodes with onboard shortcut), edge = adjacency (not utilization — that's maps' job), stale edges dashed. Purpose: network truth + weathermap suggestion source; "create map from selection" action seeds the editor.

## 8. Platform `/platform` (management)

Tabs: **Sites** (tree region→DC, device/poller counts, CRUD) · **Pollers** (fleet table: name, site, status, heartbeat age, version, polls/s, buffer depth, queue lag; enroll flow: generate token modal with copy-once token + install one-liner; approve pending; disable/re-enroll) · **Connectors** (catalog cards: vendor logo, id, version, capability badges (traffic/health/topology/…), verified-models list, device count) · **Credentials** (Admin-only: table name/kind/protocols/ref-count; create/rotate modal with write-only secret fields + test-against-device; delete blocked while referenced with device list) · **Discovery** (FR-SYNC-04): define a scan (CIDR + site + candidate credential chips), run it on demand, and work the results queue — each find shows its IP, sysDescr, auto-matched connector, and whether it is already managed; approve (with an editable name, defaulting to sysName) onboards it as a tagged `discovered` device, or ignore it · **Polling profiles** (interval matrix editor with per-family toggles, device count, min-interval guards).

## 9. Audit `/audit` (Admin + Auditor)

FilterBar: actor, action prefix (namespaced dropdown tree), resource type/id, date range, source IP. Table: time, actor (user/token/system chip), action, resource link (when live), source IP. Row → drawer: full record, before/after diff viewer (JSON diff, secrets shown as `•••` markers), trace ID (copy). Export CSV (rate-limited). Retention note displayed. Zero mutating controls by design.

## 10. Settings `/settings` (Admin)

Sections (left sub-nav): **General** (instance name, default landing, default map) · **Notifications** (channel list + create/edit per kind — SMTP form with STARTTLS/auth + test-send, webhook URL/headers/HMAC secret write-only + test, Slack webhook + test; policy list: ordered match rules severity/scope → channels, drag to reorder) · **Polling defaults** (default profile picker, global SNMP timeouts) · **Retention** (tier table raw/5m/1h with size estimates, audit/asset retention) · **Security** (password policy display, session TTLs, rate limits — read-mostly v1) · **About/System** (version, component health summary → link to System Health). Every save shows the audit-diff toast ("change recorded").

## 11. Users `/users` (Admin)

Table: username, display name, email, roles chips, status, last login; create modal (roles multi-select, temp password copy-once); detail drawer: role editor (grant/revoke audited), deactivate/reactivate, force reset, API-tokens list (revoke); My Profile page (all users): display name, email, password change, theme, timezone, personal API tokens (create scoped/expiring, copy-once).

## 12. System Health `/platform/health` (Admin — doc 22 §5)

Pipeline throughput diagram with live numbers (jobs → polls → samples → writes), per-site poller cards, queue depth charts + DLQ badges (replay affordance post-v1), data-tier vitals (PG/VM/Redis/RMQ), SLO burn-down tiles, ingest freshness. Built from the same components as the product — it's also the demo of our own dogfooding.

## 13. Accessibility & i18n

Keyboard: full table/nav operability, map editor arrow-nudge + shortcut sheet (?), focus-visible rings. Screen-reader labels on all status colorography (state announced textually). Live regions announce new critical alerts (opt-out). i18n: strings externalized from day one (react-i18next), en only v1 — retrofit-proofing, not a feature.
