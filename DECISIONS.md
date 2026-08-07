# DECISIONS.md — Architecture Decision Log

ADR-lite format. Every entry: context → decision → rationale → consequences. Newest at the bottom. **Statuses:** `accepted`, `superseded-by-ADR-nnn`, `proposed`.

Decisions ADR-001 … ADR-017 were made with the product owner on **2026-08-07** during design kickoff Q&A.

---

## ADR-001: Backend language — Go
**Status:** accepted
**Context:** Platform is dominated by high-concurrency I/O: SNMP polling of up to 100k devices, ICMP probing, queue consumers. Solo developer, AI-assisted.
**Decision:** Go for all backend services (API, scheduler, poller, ingester, alerter, notifier).
**Rationale:** goroutine-per-target polling scales cheaply (gosnmp is mature); single static binaries suit remote-site pollers; the surrounding ecosystem (VictoriaMetrics, Prometheus exporters) is Go, so reference implementations abound. One language across control- and data-plane keeps a solo dev sane.
**Consequences:** DDD/CQRS implemented with Go idioms (interfaces + constructor injection), not a framework like MediatR. Accept more hand-rolled wiring in exchange for runtime simplicity.

## ADR-002: Time-series database — VictoriaMetrics
**Status:** accepted
**Context:** Cardinality at ceiling scale ≈ 100k devices × ~50 interfaces × ~15 series ≈ 75M active series; launch scale is <500 devices. Cacti/RRD model explicitly rejected.
**Decision:** VictoriaMetrics, single-node for v1, `vmcluster` when scale demands.
**Rationale:** PromQL-compatible (MetricsQL), best-in-class compression and RAM at high cardinality, free clustering, trivial ops (one binary), native `influx` and `remote_write` ingestion endpoints for pollers to push to.
**Consequences:** Dashboard queries use MetricsQL via VM's HTTP API, proxied by the core API. Downsampling via `vmalert` recording rules (free tier lacks automatic downsampling).

## ADR-003: Relational database — PostgreSQL 16
**Status:** accepted
**Context:** Inventory, credentials, alert state, audit, settings need ACID + rich querying.
**Decision:** PostgreSQL 16 for all non-time-series state.
**Rationale:** Row-level security ready for future multi-tenancy; JSONB for connector-specific attributes; mature operators for on-prem k8s (CloudNativePG).
**Consequences:** One relational store shared by all services in v1 (schema-per-context to keep the microservice seam).

## ADR-004: Launch scale <500 devices; design ceiling 100k
**Status:** accepted
**Decision:** Build the *pipeline shape* for 100k (queue-based ingestion, site-sharded pollers, stateless services) but deploy v1 minimal (single poller per site, single-node VM/PG).
**Rationale:** Scale-out must be a deployment change, not a redesign; but building sharding/clustering now would sink a solo dev.
**Consequences:** Capacity triggers documented in doc 04 (NFR) tell us when to activate clustering.

## ADR-005: Operating model — self-hosted, single organization
**Status:** accepted
**Decision:** v1 is single-tenant self-hosted. Every table carries `tenant_id` (default tenant), every query goes through a tenant-scoped repository, but no tenant management UI/API exists.
**Rationale:** Multi-tenant retrofits are the classic SaaS rewrite trigger; carrying a dormant `tenant_id` is nearly free.

## ADR-006: Kubernetes on-prem
**Status:** accepted
**Decision:** Target vanilla-conformant on-prem Kubernetes; recommend RKE2 (or k3s for lab). Helm is the only supported install mechanism. 4–5 datacenters: one **core site** runs the full stack; other sites run only a poller deployment phoning home.
**Consequences:** Remote pollers must work from behind NAT: all connections are outbound from the poller (AMQP over TLS to core RabbitMQ). No inbound ports at remote sites.

## ADR-007: v1 metric scope
**Status:** accepted
**Decision:** v1 collects categories 1 (Interface/Traffic), 2 (Device Health), 3 (Availability/Latency), 12 (Inventory metadata + LLDP/CDP topology). Categories 4–5 (L2/L3) next; 6–11 (firewall, wireless, hosts, synthetic, facilities, flow) are roadmap (doc 29).
**Rationale:** Categories 1/2/3/12 cover ~90% of NOC value and 100% of the weathermap's needs.

## ADR-008: Weathermap is the v1 flagship
**Status:** accepted
**Decision:** An editable topology map (drag nodes, draw/auto-import links, links colored by live utilization, nodes colored by state) ships in v1, custom-built in React (@xyflow/react + custom SVG edge rendering).
**Rationale:** It is the product's identity — the Cacti Weathermap succession is the reason this project exists.

## ADR-009: All visualization custom-built; no Grafana embed
**Status:** accepted
**Decision:** Dashboards/charts are custom React components; **uPlot** for time-series charts (high density, tiny footprint), custom SVG/Canvas for weathermap and heatmaps.
**Consequences:** More frontend effort (budgeted in sprints 11–16); full control over UX and licensing.

## ADR-010: Auth v1 — local accounts + JWT, OIDC-shaped
**Status:** accepted
**Decision:** Argon2id password hashing; short-lived JWT access tokens (15 min) + rotating refresh tokens; JWT claims mirror OIDC (`sub`, `preferred_username`, `roles`) so Keycloak can later become the issuer with minimal change. Personal API tokens for automation.
**Consequences:** The API validates tokens via a pluggable `TokenVerifier` interface — swapping issuer = config change.

## ADR-011: Credential storage — app-level envelope encryption
**Status:** accepted
**Context:** No Vault/OpenBao available on-prem.
**Decision:** SNMP credentials encrypted with AES-256-GCM using per-row data keys, wrapped by a master key supplied via Kubernetes Secret (env). Key-rotation procedure documented in doc 20.
**Consequences:** Master key custody is an operational duty (doc 20 §key management). A future `KeyProvider` implementation can delegate to Vault without schema change.

## ADR-012: Messaging topology — RabbitMQ
**Status:** accepted
**Decision:** RabbitMQ carries (a) poll job dispatch on per-site quorum queues, (b) metric batches from pollers to the ingester, (c) domain events on a topic exchange, (d) notification fan-out. Details in doc 05 §messaging.
**Rationale:** Required by product owner; per-site queues give remote-poller isolation and backpressure for free.

## ADR-013: Background jobs — scheduler service + queue workers
**Status:** accepted
**Decision:** A dedicated `scheduler` service owns all recurring work (poll cycles, discovery, retention, report jobs) using cron-style schedules persisted in Postgres, dispatching work as RabbitMQ messages consumed by stateless workers. No Temporal/external engine.
**Rationale:** One less stateful dependency; the job model here (fixed-interval fan-out) doesn't need workflow durability semantics.

## ADR-014: Connector framework — compile-time plugin registry
**Status:** accepted
**Decision:** Each vendor connector is a Go package under `connectors/` implementing the interfaces in doc 10, self-registering via an `init()` registry. Adding a platform = new package + one import line in `connectors/registry`. Out-of-process connectors (hashicorp/go-plugin over gRPC) are a documented future seam for third-party/other-language connectors.
**Rationale:** Go's native `plugin` package is fragile (exact-toolchain requirement); compile-time registration is how Terraform-era systems started, and the interface boundary keeps the future extraction cheap.

## ADR-015: Repo layout — monorepo
**Status:** accepted
**Decision:** Single repo: `docs/`, `backend/`, `connectors/`, `frontend/`, `deploy/`.
**Rationale:** AI-native requirement — one clone gives any agent complete context; solo dev has no cross-team versioning needs.

## ADR-016: CI/CD — GitHub Actions → GHCR
**Status:** accepted
**Decision:** GitHub Actions for lint/test/build/scan; images to GitHub Container Registry; Helm chart published as OCI artifact; deploys pulled by the on-prem cluster (no inbound access from GitHub to on-prem). Pipeline detail in doc 25.

## ADR-017: Architecture style — modular monolith, microservice-ready
**Status:** accepted
**Decision:** One Go module with clean bounded-context packages, deployed as **six processes** sharing that codebase (api, scheduler, poller, ingester, alerter, notifier). Contexts communicate through interfaces + events, never cross-package DB access.
**Rationale:** Six binaries give independent scaling (the microservice benefit that matters here) without six repos/pipelines (the cost that kills solo devs). True service extraction later follows the seams.
