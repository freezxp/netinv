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
**Amended (pilot):** superseded in one narrow respect by ADR-018 — a wireless *controller* connector reporting client and AP counts is in v1. The category-6 deferral stands for everything else wireless.

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

## ADR-018: Wireless controller counts are in scope, per-client wireless is not
**Status:** accepted (pilot, supersedes part of ADR-007)
**Decision:** A controller connector may report estate-level wireless gauges — connected clients, APs up/total — and the UI surfaces them on a Wireless tab that appears only for devices reporting them. Per-client tables, radio/RF metrics, roaming and WLAN configuration remain deferred to category 6 (doc 29).
**Rationale:** The pilot runs a Ruckus Unleashed master that exposes *no* CPU, memory or temperature anywhere. Under the original scope it was monitorable only as an ICMP target, while the two numbers that describe its health — how many clients it carries, and whether every AP is up — were sitting in a MIB it already answered. Excluding them was a rule with no benefit.
**Consequences:** The line is estate-level gauges only. Anything needing per-client or per-radio state still waits for category 6, so this does not quietly become a wireless product. `netinv_wireless_*` is the metric namespace; an AP-down alert is expressed as `netinv_wireless_ap_up_count < netinv_wireless_ap_total`.

---

## ADR-019: Licence — Apache-2.0, and the repository is public
**Status:** accepted (2026-08-09)
**Context:** NetInv was developed in a private repository. It monitors network equipment, and the people who could most improve it are the people who own hardware it has never been tested against — four of the seven connectors have only ever met MIB specifications and recorded fixtures. That validation cannot be bought or simulated; the snmpsim fixtures have already been exhausted as a source of new findings, and every gap since has come from real devices. A private repository makes the one contribution the project actually needs impossible to offer.
**Decision:** Publish under the Apache License 2.0. Contributions are accepted under the same licence, with no CLA.
**Rationale:** Apache-2.0 over MIT for the explicit patent grant — NetInv implements vendor MIB access, an area with real patent activity, and a bare copyright licence leaves contributors and deployers exposed. Apache-2.0 over AGPL because the goal is deployment breadth: a network team is the target user, and many organisations' policies prohibit AGPL outright, which would exclude exactly the operators whose hardware reports the project depends on. Protecting against a hosted closed fork is a real concern but a smaller one than never learning that the Huawei connector reports nonsense.
**Consequences:**
- Every shipped dependency must stay permissively licensed; `make licenses` enforces this in CI and fails on GPL/AGPL/SSPL. Build-time-only copyleft (lightningcss) is permitted and reported, since it never reaches a user's machine.
- The public repository documents deployment posture honestly, including that the quickstart serves plain HTTP and that the doc 20 §12 security checklist has not been run against TLS (see SECURITY.md). Publishing a monitoring tool with undisclosed hardening gaps would be worse than not publishing it.
- Live pilot network detail was removed from CLAUDE.md, and the Ruckus test fixture — which had been recorded verbatim from the pilot's R710, serial and hostname included — was replaced with synthetic values. **The current tree is clean; commit message bodies are not.** Four items survive in history: two private-range addresses (`10.0.31.8`, `10.0.30.0/24`), the R710's serial number, and the AP's hostname. Rewriting them was prepared twice and declined both times, the second time knowingly: the rewrite would change the hashes of the 33 newest commits, breaking every clone and invalidating the hashes cited in `.gitleaks.toml` and doc 25 §5.1, against an exposure of two unroutable subnets plus a serial whose practical use is a vendor warranty lookup on one access point.
- **This is a judgement about these four specific items, not a policy.** Nothing routable, no credential, and nothing naming the owner's organisation has ever been committed, and none may be. The lesson worth carrying forward is narrower and sharper: the serial reached the repository because a connector test fixture was recorded from real hardware and committed as-is, which is exactly what doc 10 §6 asks contributors to do. Recorded walks make excellent fixtures and terrible publications — redact identity fields (serial, hostname, addresses) when committing one, and say so in the hardware-report template.
- GitHub Actions on public repositories are free on standard runners, which incidentally resolves the billing block that had been preventing CI from running at all.

---

## ADR-020: Flow collection is aggregate-only; raw per-flow storage stays deferred
**Status:** accepted (2026-08-10, amends ADR-004's category-11 deferral)
**Context:** "What is actually filling this link?" is the question interface counters cannot answer, and it is the most common follow-up to a utilization alert. Flow export (NetFlow v5/v9, IPFIX, sFlow) answers it. The reason it was deferred is not that it is unwanted but that flow data is shaped nothing like the rest of the platform: a flow record is a wide event keyed by source, destination, port and protocol, and storing those as time series would multiply cardinality by every host pair on the network. A single busy link can produce more distinct series in an hour than the entire device fleet produces in a year.
**Decision:** Add a `netinv-flow` collector that receives NetFlow and sFlow and **aggregates at ingest**, writing only bounded-cardinality series to VictoriaMetrics: top-N talkers, conversations and applications per interface per interval. Raw per-flow records are not stored. Full per-flow retention with a columnar store (ClickHouse) remains deferred and needs its own ADR.
**Rationale:** The aggregate answers the operational question — which hosts, which conversations, which ports are consuming a link — at a cardinality the existing stack already handles, with no new datastore to run, back up or secure. That matters more here than completeness: every stateful dependency added is one an operator must understand, and ADR-013 already chose fewer of them deliberately. The forensic questions raw flows answer ("every flow from this host last Tuesday") are a different product with a different cost, and conflating the two is how a monitoring tool acquires a database it cannot justify.
**Consequences:**
- Cardinality is bounded by construction: N talkers × interfaces, not host-pairs. The top-N cut happens in the collector, before anything is stored, and is therefore not recoverable later — a conversation outside the top N for an interval did not merely go unqueried, it was never written.
- Flow is *sampled* on sFlow and often on NetFlow too. Volumes derived from it are estimates with real error bars, and the UI must not present them beside SNMP counters as though both were measured the same way. SNMP remains the authority for how much traffic crossed an interface; flow answers what it consisted of.
- The collector is a separate service and a separate listening socket (UDP 2055/4739/6343). It is the first component that accepts unsolicited input from the network rather than initiating its own connections, which is a materially different exposure: doc 34 §6 covers the source allow-list and the resource bounds that follow from it.
- Deployments with no exporter get nothing, and that is expected. Nothing in the reference pilot emits flow — UniFi gateways do not export it natively and none of the switches advertise the sFlow MIB — so this feature was built and validated against a generated source.

---

## ADR-021: Enterprise firewalls are in scope as devices; policy-level firewall metrics are not
**Status:** accepted (2026-08-11, extends ADR-002's vendor list and amends part of ADR-004 category 11)
**Context:** v1 named six platforms — Cisco, Juniper, Huawei, ZTE, Ubiquiti, Ruckus — and deferred "firewall/NAT/LB metrics" wholesale. Those are two different exclusions collapsed into one line, and only one of them was ever justified. A FortiGate or a PA-series appliance sitting at a site perimeter is, for monitoring purposes, a device with interfaces, a CPU, memory, sensors and an uptime; excluding it means the busiest link in a branch office is the one NetInv cannot draw. What genuinely warrants deferral is the *policy* surface — per-rule hit counts, NAT pool occupancy, VPN tunnel tables, threat logs — which is a different data model, a different UI, and in several cases not SNMP at all.
**Decision:** Add `fortinet-fortios` and `paloalto-panos` connectors covering the same ground every other connector does — inventory, interfaces, health — plus **device-level session gauges**, which are the one firewall-specific numbers that answer "is this box in trouble": active sessions, the platform's session ceiling where it publishes one, and session setup rate where available. Everything policy-, rule-, NAT- or VPN-scoped stays deferred to doc 29 category 11.
**Rationale:** This is ADR-018's reasoning applied to a second area. There, a controller exposed no CPU or memory anywhere, and the two numbers describing its health were sitting in a MIB it already answered; excluding them was a rule with no benefit. The same holds here: a firewall at 95% of its session table is in trouble in a way no interface counter shows, the value is a single scalar in a MIB the device already serves, and it costs one gauge. The line is drawn at the point where the data stops being a number about the box and starts being a table about its configuration — that is where the different data model, and the real work, begins.
**Consequences:**
- `netinv_firewall_*` is the metric namespace: `netinv_firewall_session_count`, `_session_max`, `_session_setup_rate`. Session *utilization* is expressed as a query against count and max rather than stored, so it cannot drift from them.
- Six platforms become eight. Two of the eight now have no realistic path to hardware validation in this project, joining `cisco-ios`, `juniper-junos`, `huawei-vrp` and `zte-zxr`: **six of eight connectors are unvalidated against real units**, and doc 10 must keep saying so per connector rather than in a footnote.
- ~~**Neither platform can feed the Flow tab.**~~ **Resolved by ADR-022 (2026-08-11):** this was true when written — FortiOS exports v9/IPFIX/sFlow and PAN-OS exports v9, and the collector decoded only v5 — and it is what made v9 the next thing built rather than a format left on a list. Both platforms now feed the Flow tab.
- Admitting session counts reopens category 11 by a measured amount. The test for anything further is the one above: a scalar about the appliance is in; a table about its policy is not, and needs its own ADR to become so.

---

---

## ADR-022: NetFlow v9 is decoded, which makes the collector stateful
**Status:** accepted (2026-08-11, extends ADR-020)
**Context:** ADR-020 shipped v5 only, on the reasoning that v5 is the one format decodable without per-exporter state. That was the right first increment and the wrong permanent position: ADR-021 then added two firewall connectors, and neither platform can export v5 at all — FortiOS does v9/IPFIX/sFlow, PAN-OS does v9. The devices whose traffic composition an operator most wants were precisely the ones the collector could not read. v5 is also IPv4-only, so a dual-stack link was silently half-reported.
**Decision:** Decode NetFlow v9, including options templates, and apply the sampling interval an exporter announces through them. IPFIX and sFlow remain undecoded.
**Rationale:** v9 is where the hardware is, and the template machinery it forces is the same machinery IPFIX will need — building it once buys both. Options templates are not optional extra credit: sampling on v9 is normally declared in an options data record rather than on the flow, so a decoder that skips them under-reports by the sampling rate with nothing anywhere indicating a problem. That is the failure mode this collector is built to refuse.
**Consequences:**
- **The collector is now stateful.** A template names the fields a later data record will contain; without it the record is an opaque byte run. State means a cache, an expiry, a bound, and a policy for data that outruns its template.
- **A restart loses every template**, and flow stays missing until each exporter resends — commonly 10-20 minutes. Nothing is wrong during that window, so it is counted and reported as `awaiting_template` rather than as an undecodable packet: filing it under "malformed" would send an operator hunting a fault that does not exist.
- **Templates are attacker-influenced state on an unauthenticated UDP port.** A spoofed source can mint a new `(exporter, observation domain, template ID)` per packet, so the cache is capped (10 000) and entries expire (60 min). When full of live templates it refuses new ones rather than evicting a working one — degrading what is learned next, never what is already working.
- **v9 carries IPv6**, so dual-stack links are reported whole for the first time. The aggregator needed no change; addresses were already strings.
- IPFIX is now a smaller job than it was — same template model, different header and enterprise-field handling — and sFlow still needs a decoder of its own.
