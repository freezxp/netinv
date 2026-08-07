# CLAUDE.md — AI Agent Onboarding

You are working on **NetInv**, a network asset monitoring platform. This file tells you everything you need to resume work with zero prior conversation context.

## Current state (update this section whenever it changes)

- **Phase: DESIGN.** The repo contains a 30-document design package in `docs/`. There is intentionally **no application code yet** — do not start coding unless the user explicitly moves the project to the build phase.
- Next milestone: design sign-off, then Sprint 1 per `docs/27-sprint-planning.md`.
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
