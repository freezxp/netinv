# 27 — Sprint Planning (20 × 2-week sprints)

**Status:** draft · **Depends on:** 26 · Team: solo developer + AI pair (Claude)

Working agreements: sprints end with a tagged, deployable state and a demo note in `docs/sprint-notes/` (created during build phase). Velocity assumption: AI-accelerated solo dev ≈ small team on well-specified work — *this design package is the specification*; sprints reference doc numbers instead of restating detail. Rule of thumb per sprint: ~70% planned scope, 30% slack for discovery/debt (solo projects with 100% planned scope always slip). Every sprint includes: tests to DoD (doc 24 §6), docs updated (NFR-70).

| S | Theme | Deliverables (docs) | Exit demo |
|---|---|---|---|
| **1** | Repo & toolchain | Monorepo dirs (12), Go module + 6 cmd skeletons with config/logx/healthz (13), Vite app shell, docker-compose with PG/Redis/RMQ/VM/snmpsim, Makefile, ci.yml lint+unit (25) | `make dev` boots everything; CI green |
| **2** | Persistence & platform kernel | goose migrations 0001–0002 (08 schemas + seeds), pgx+sqlc setup, `platform/{amqpx,redisx,cryptox,errx,retryx}` with unit tests, envelope crypto (20 §7) | migration up/down; crypto round-trip tests |
| **3** | AuthN | Login/refresh/logout/me + Argon2id + JWT + lockout (03 FR-AUTH, 07 §4), audit writer + auth events, OpenAPI generation wired (25) | curl login → JWT → /auth/me; lockout works |
| **4** | AuthZ & core CRUD | RBAC middleware + permission matrix (20 §5), users/roles endpoints, sites, credentials vault + test action (09 §3–5), generated authz-matrix tests (24) | 4 roles × endpoints test green |
| **5** | Devices & scheduler | Devices/interfaces/groups CRUD + CSV import + search indexes (09 §6), polling_schedule + scheduler service with leader election + job publish (05 §5) | jobs visible on site queue for enabled device |
| **6** | Poller & generic connector — **M1** | Poller runtime (workers, SNMP session, batcher, disk buffer) (13 pollerrt), connector SDK + registry + generic connector IF-MIB/system (10), ingester→VM with enrichment + derivations (05 §5–6), pipeline integration test (24) | simulated device onboarded → graphs in VM <2 min; RMQ restart survival |
| **7** | ICMP + poller fleet | ICMP prober (jitter/loss), poll-success metrics, poller enrollment/approve/heartbeat (FR-PLT-02), remote-poller compose profile (18) | second "site" polling via enrolled poller |
| **8** | Sync engine | SyncDiffer + identity resolution + asset history + missing-N (11), sync runs API, on-demand sync, reboot-triggered resync, LLDP topology capture | change a sim device → history rows + events |
| **9** | Alerting & notify — **M2** | Alerter (eval loop, lifecycle, fingerprints, flap) (05, 16 §4), builtin rule pack, rules/alerts/silences API (09 §9), notifier email+webhook+Slack with retries/DLQ (23 §2), deliveries log | sim failure → Slack <60 s; ack via API |
| **10** | Backend hardening buffer | Dashboard aggregate refresher + endpoints (09 §8), events stream (P1) backend, DLQ replay CLI, self-metrics on all services (22 §1), catch-up slack (planned!) | dashboard JSON endpoints live; backlog zero |
| **11** | Frontend foundation | AppShell/nav/theme/dark mode, auth flow + token refresh, generated API types + hooks (14), login + inventory list w/ virtualized table, filter bar → URL state | login → browse inventory in browser |
| **12** | Dashboard I | Status row, active alerts panel (severity sort, ack), TimeSeries/uPlot wrapper, aggregate bandwidth + latency graphs (30 §2) | NOC wall v0 on demo data |
| **13** | Dashboard II + device detail | Top-N lists, health heatmap, capacity watchlist, device detail page (identity, interface table + sparklines, health, alerts, history) (30 §4–5) | alert → 1-click → explaining graph (G3) |
| **14** | Inventory complete — **M3** | CSV/XLSX export jobs (09 §13), asset history timeline UI, device onboarding/edit forms + credential picker, sim-fleet demo dataset polish | end-to-end product walkthrough |
| **15** | Weathermap editor | React Flow canvas, node palette/search-picker, link drawing + endpoint binding, undo/redo, autosave draft, publish flow (30 §3, FR-MAP) | build the backbone map by hand |
| **16** | Weathermap live — **M4** | Live-data endpoint + cache (05 §7), split-direction utilization edges + classic scale, node state colors, LLDP suggestions, map export/import, viewer mode | live-colored map on the wall, <30 s refresh |
| **17** | Vendor connectors | cisco-ios, juniper-junos, huawei-vrp health/optics/components; zte + ubiquiti best-effort (10 §5); real-hardware validation passes; connector catalog UI | 5-vendor sim + ≥3 real devices with health data |
| **18** | Admin & settings UI | Users/roles UI, settings (SMTP/channels/policies + test-send), audit viewer + export, poller fleet page, silences/maintenance UI, PDF export (P1 if time) | full admin loop in browser |
| **19** | Hardening | 500-device load run + fixes (NFR §2), soak 72 h, chaos-lite (24 §4), security checklist (20 §12), backup/restore drill + runbooks, Helm chart polish + quickstart <30 min test, docs as-built sweep | all release gates green except pilot |
| **20** | Pilot & release — **M5** | Deploy owner's 4–5 sites, onboard real estate, 2-week burn-in (starts mid-S19 if green), fix pilot findings, v1.0.0 tag + release notes, DECISIONS/docs final sweep | **v1.0 in production** |

## Backlog discipline

- Anything cut mid-sprint moves to the next sprint's 70% or explicitly to post-v1 (doc 29) — never silently dropped; DECISIONS.md records scope changes.
- P1 items (discovery UI, maintenance windows, PDF, events stream UI, background images) live in a ranked backlog; they enter S10/S18 slack only when the sprint's P0 scope is done.
- Two named contingencies: if **weathermap** slips, S17 vendor depth donates a week (flagship beats breadth); if **vendor MIBs** surprise (R-07), ZTE/Ubiquiti health ships best-effort-generic and is documented as such — never blocks release.
