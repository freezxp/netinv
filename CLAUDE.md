# CLAUDE.md — AI Agent Onboarding

You are working on **NetInv**, a network asset monitoring platform. This file tells you everything you need to resume work with zero prior conversation context.

## Current state (update this section whenever it changes)

- **Phase: PILOT — all 20 sprints complete, running live on the owner's network.** M1–M4 achieved. Post-sprint work is field-driven: real hardware keeps finding gaps the snmpsim fixtures could not, and each one is fixed with a regression test rather than a workaround.
- **Live pilot fleet** (do not assume these are demo data): 4 UniFi gateways in a full SD-WAN mesh — FN 10.0.30.1, AL 10.0.29.1, AA 192.168.104.1, YY 192.168.100.1 — plus a Ruckus Unleashed R710 master at 10.0.31.8 with one mesh-joined member AP. A weathermap "SD-WAN mesh" covers all six WireGuard tunnels with live traffic.
- **Validated against real hardware** (risk R-07, partially closed): `ubiquiti` and `generic` on UDM-Pro/UCG-Ultra, `ruckus` on an R710. Each is documented in doc 10 with what the device *actually* exposes — including what it does not. **`cisco-ios`, `juniper-junos`, `huawei-vrp` and `zte-zxr` remain unvalidated against real units.**
- **What remains for v1.0:** real-hardware validation of the four untested vendor connectors, 72 h soak + chaos-lite on staging k8s, the doc 20 §12 security checklist against TLS, and a backup/restore drill on the pilot data. These need real infrastructure and time — do not fake them.
- **Recurring lesson worth reading before touching metrics code:** MetricsQL `or` matches on labels *excluding* `__name__`, so two metrics that differ only by name collapse into one and the second disappears silently. This has now caused three separate bugs (traffic in/out, memory used/total, AP up/total). Combine named metrics through `trafficExpr`/`seriesExpr` in `frontend/src/api/hooks.ts`, never a bare `or`.
- Dev loop: `make dev` boots infra (PG/Redis/RabbitMQ/VM/snmpsim/mailhog); run services with `make run-<svc>`; api+scheduler+notifier need the same `NETINV_MASTER_KEY`; snmpsim devices use `snmp_port: 1161` and communities `public` (generic) / `cisco` (cisco-ios fixture). `scripts/seed-demo.sh` seeds a demo fleet.
- Code must follow the design docs; divergence requires updating the doc in the same commit (NFR-70).
- Owner: solo developer (GitHub `freezxp`), pairing with AI. Assume the AI does most of the writing; keep everything reproducible from the repo alone.

## Non-negotiable decisions

All architecture decisions live in **`DECISIONS.md`** with rationale. Never silently contradict one; if a decision needs revisiting, propose a change there first. Headlines:

- Go backend, React+TS frontend, VictoriaMetrics + PostgreSQL + Redis + RabbitMQ, REST API, on-prem Kubernetes, GitHub Actions CI.
- v1 scope: SNMP v2c/v3 collection of interface traffic, device health, ICMP availability, inventory — for Cisco, Juniper, Huawei, ZTE, Ubiquiti and Ruckus. **Weathermap editor is the v1 flagship.**
- Explicitly deferred: firewall/NAT/LB metrics, NetFlow, syslog/trap ingestion, Keycloak, multi-tenant activation, HA. Deferred ≠ ignored: the design keeps seams for each. **Wireless is now partly in scope** — controller-level client and AP counts only, per ADR-018; per-client and RF metrics remain deferred.
- Clean Architecture + DDD bounded contexts + CQRS-lite (separate read/write paths, single Postgres). Connector plugin framework with compile-time registration (no Go `plugin` package).

## How to work in this repo

1. **Read before writing:** `DECISIONS.md`, then the specific doc you're touching, then adjacent docs it cross-references.
2. **Docs are the source of truth.** When code exists later, code must follow docs; if reality diverges, update the doc in the same commit.
3. Each doc in `docs/` has a frontmatter-style header (`Status`, `Depends on`). Keep statuses honest: `draft` → `review` → `approved`.
4. Mermaid for all diagrams. Markdown tables for schemas/APIs. No binary artifacts in `docs/`.
5. Conventional commits (`docs:`, `feat:`, `fix:`, `chore:`). One logical unit per commit.
6. Terminology is defined in `docs/16-domain-model.md` (ubiquitous language). Use those exact terms — e.g. a *Device* is a managed network element; a *Poller* is a site-local collection agent; a *Connector* is a vendor driver.

## Doc package map

`docs/README.md` is the authoritative index of all 30 documents with one-line summaries and dependency order. Documents are numbered `01`–`30`; the number is the canonical reference (e.g. "per doc 09" = API spec).

## When the build phase starts

- Backend scaffolding goes in `backend/` as a Go workspace; connectors in `connectors/`; follow `docs/13-backend-structure.md` exactly.
- First runnable target is defined in Sprint 1 of `docs/27-sprint-planning.md`.
- Add per-directory CLAUDE.md files as code directories appear.
