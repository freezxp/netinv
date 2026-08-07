# CLAUDE.md — AI Agent Onboarding

You are working on **NetInv**, a network asset monitoring platform. This file tells you everything you need to resume work with zero prior conversation context.

## Current state (update this section whenever it changes)

- **Phase: BUILD — Sprints 1–19 of 20 complete** (commits tagged `feat: sprint N — …`; every sprint has a verified live exit demo recorded in its commit message). M1–M4 all achieved.
- **What remains for v1.0 (Sprint 19 tail + 20):** real-hardware validation of vendor connectors (risk R-07), 72h soak + chaos-lite on a staging k8s cluster, security checklist (doc 20 §12), backup/restore drill, and the pilot deployment across the owner's 4–5 sites. These need real infrastructure/time — do not fake them.
- Dev loop: `make dev` boots infra (PG/Redis/RabbitMQ/VM/snmpsim/mailhog); run services with `make run-<svc>`; api+scheduler+notifier need the same `NETINV_MASTER_KEY`; snmpsim devices use `snmp_port: 1161` and communities `public` (generic) / `cisco` (cisco-ios fixture). `scripts/seed-demo.sh` seeds a demo fleet.
- Code must follow the design docs; divergence requires updating the doc in the same commit (NFR-70).
- Owner: solo developer (GitHub `freezxp`), pairing with AI. Assume the AI does most of the writing; keep everything reproducible from the repo alone.

## Non-negotiable decisions

All architecture decisions live in **`DECISIONS.md`** with rationale. Never silently contradict one; if a decision needs revisiting, propose a change there first. Headlines:

- Go backend, React+TS frontend, VictoriaMetrics + PostgreSQL + Redis + RabbitMQ, REST API, on-prem Kubernetes, GitHub Actions CI.
- v1 scope: SNMP v2c/v3 collection of interface traffic, device health, ICMP availability, inventory — for Cisco, Juniper, Huawei, ZTE, Ubiquiti. **Weathermap editor is the v1 flagship.**
- Explicitly deferred: wireless controllers, firewall/NAT/LB metrics, NetFlow, syslog/trap ingestion, Keycloak, multi-tenant activation, HA. Deferred ≠ ignored: the design keeps seams for each.
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
