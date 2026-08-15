# 05 — System Architecture

**Status:** draft · **Depends on:** DECISIONS.md, 04 · **Constrains:** 06–19

## 1. Architectural style

**Modular monolith, deployed as six processes** (ADR-017). One Go module; bounded contexts (doc 16) as packages with enforced boundaries (no cross-context imports except via `internal/platform` and published interfaces); six `cmd/` binaries share the codebase but run and scale independently. This is "microservice-ready": the deployment topology, messaging, and data-ownership rules are already service-shaped — extraction later is a repo split, not a redesign.

**Clean Architecture layering** inside each context:

```
domain/        entities, value objects, domain services   — imports nothing
application/   use cases (commands + queries), ports       — imports domain
adapters/      repositories (Postgres), VM client, AMQP,   — implements ports
               SNMP, HTTP handlers
cmd/           composition roots: wire adapters into apps  — imports everything
```

Dependency rule: source arrows point inward only. DI is constructor injection wired in `cmd/` (no DI container; Go idiom, ADR-001).

**CQRS-lite:** commands and queries are separate application-layer types with separate handler pipelines (validation → authorize → audit for commands; cache-aware for queries). One PostgreSQL instance backs both; read models are Redis-cached projections, not a separate store. Rationale: full CQRS with separate read stores is unjustified at v1 scale but the handler split preserves the seam (NFR capacity trigger >50k devices splits deployments).

## 2. The seven services

| Service | Binary | Role | State | Scaling |
|---|---|---|---|---|
| **API** | `netinv-api` | REST API, authN/Z, RBAC, all CRUD, query proxy to VM, dashboard aggregate assembly, sync-result consumer | stateless | horizontal (v1: 2 replicas) |
| **Scheduler** | `netinv-scheduler` | Owns time: computes due poll/sync/discovery jobs from Postgres schedules, publishes to per-site queues; leader-elected singleton | leader lease in Redis | active/standby |
| **Poller** | `netinv-poller` | Site-local agent: consumes its site's job queue, executes SNMP/ICMP via connectors, batches results, publishes home | local disk buffer only | per-site horizontal |
| **Ingester** | `netinv-ingester` | Consumes metric batches: validates, enriches with inventory labels, computes derived metrics, writes VM; emits state-transition events | stateless | horizontal |
| **Alerter** | `netinv-alerter` | Evaluates alert rules against VM (MetricsQL), manages alert lifecycle in PG, emits `alert.*` events; leader-elected | leader lease | active/standby |
| **Notifier** | `netinv-notifier` | Consumes `alert.*` + routing policies → Email/Webhook/Slack, retries, delivery log | stateless | horizontal |
| **Flow** | `netinv-flow` | Receives NetFlow v5/v9 and IPFIX on UDP 2055/4739, aggregates each interval to top-N talkers/conversations/applications and writes them to VM (ADR-020, doc 34) | v9/IPFIX template cache, in memory | **single replica** |

The flow collector is the one service that does not scale horizontally, and the
reason is worth stating rather than discovering: flow arrives over UDP with no
delivery guarantee and no way to replay, and v9/IPFIX state is per exporter. A
second replica would receive half of one exporter's datagrams and none of its
templates, and decode nothing. Scaling means partitioning exporters across
collectors, which needs its own design. It is also the only service that accepts
unsolicited input from the network rather than initiating its own connections
(doc 34 §6).

Sync logic (doc 11) lives as an application service inside the API's Inventory context v1 (results arrive via queue from pollers); the seam allows extracting `netinv-sync` later.

## 3. Data stores & ownership

| Store | Owner(s) | Content |
|---|---|---|
| PostgreSQL 16 | API (sole writer for config/inventory), Alerter (alert state), Notifier (delivery log) | Everything in doc 08; schema-per-context (`iam`, `inventory`, `alerting`, `audit`, `platform`) |
| VictoriaMetrics | Ingester (sole writer), Alerter+API (readers) | All time series; label schema §6 |
| Redis | shared, namespaced | dashboard aggregate cache, query cache, rate limits, login lockouts, distributed locks (`SET NX PX`), leader leases, refresh-token denylist |
| RabbitMQ | topology below | jobs, metric batches, domain events, notifications |
| Poller local disk | each poller | bounded overflow buffer (FR-COLL-08) |

**Rule: contexts never touch another context's schema.** Cross-context reads go through the owning context's Go interface; cross-service via events or REST.

## 4. Messaging topology (RabbitMQ, ADR-012)

| Exchange | Type | Queues | Purpose |
|---|---|---|---|
| `jobs.poll` | direct, rk=`site.<site_id>` | `poll.site.<site_id>` (quorum, TTL 2× interval) | scheduler → site pollers. Per-site queue = site isolation, natural backpressure, exactly-one-consumer-group per FR-COLL-07. Message TTL discards stale jobs rather than hammering devices after an outage (NFR-19). |
| `metrics.ingest` | direct | `metrics.raw` (quorum, lazy) | pollers → ingesters. Batch payloads (§5), publisher-confirmed, consumer-acked after VM write success → at-least-once; VM writes are idempotent (same timestamp+labels+value). |
| `events.domain` | topic | per-consumer queues, rk e.g. `inventory.device.changed`, `metrics.state.transition`, `alert.fired`, `sync.completed` | event-driven synchronization (requirement): decouples producers from alerting, audit, cache invalidation, future integrations |
| `notify.dispatch` | direct | `notify.email`, `notify.webhook`, `notify.slack` (+ DLQ each) | alert → channel delivery with per-channel retry/DLQ |

All queues are quorum queues (durable, replicated when RabbitMQ is clustered). Every queue has a paired DLQ; DLQ depth is an alert on ourselves (doc 22). Remote pollers connect **outbound** AMQPS 5671 to core (ADR-006); poller AMQP credentials are per-site, minimally scoped (doc 20 §8).

## 5. Collection pipeline (the data plane)

```
scheduler ──jobs──▶ RabbitMQ ──▶ poller ──SNMP/ICMP──▶ devices
                                   │ batches (protobuf, zstd)
                                   ▼
                              metrics.raw ──▶ ingester ──HTTP──▶ VictoriaMetrics
                                                │ state transitions
                                                ▼
                                          events.domain ──▶ alerter / API cache invalidation
```

- **Job granularity:** one job = one device × one metric family (traffic | health | icmp | inventory-sync). Keeps jobs small, retryable, and evenly distributable; a slow device can't stall a batch.
- **Scheduler algorithm:** every 10 s, `SELECT` due work from `polling_schedule` (next_due_at ≤ now, device enabled, site active), publish jobs, advance `next_due_at` by interval with per-device jitter (hash-spread) to flatten load. Leader election via Redis lease prevents double-publishing; publishes are idempotent anyway (poll job executed twice = wasted poll, not corruption).
- **Poller concurrency:** worker pool (default 200 goroutines); per-device semaphore of 1 (FR-COLL-10); SNMP timeout 5 s × 2 retries within the poll before reporting `timeout`.
- **Batch format:** protobuf `MetricBatch{poller_id, samples[]{device_id, name, labels, ts, value}}`, zstd-compressed; ~500 samples or 5 s flush.
- **Ingester enrichment:** joins device_id → stable labels (device name, site, vendor, role) from an in-memory inventory snapshot refreshed on `inventory.*` events. Metric identity uses `device_id`/`if_index` with human labels as convenience — renames don't create new series. **`if_index` is stable enough to key a series, but it is not a stable identifier**: agents renumber across reboots, and a renumbered interface therefore starts a new series rather than continuing the old one. That is correct for a time series — the two really are different counters — but it means nothing outside VictoriaMetrics may pin an ifIndex to identify an interface. Entities are keyed by the interface row id, and consumers resolve the current index from it (doc 11 §3.1).
- **Derived at ingest:** utilization % (needs previous counter sample — ingester keeps a small LRU of last counter values; also computed at query time via `rate()` as fallback), counter-wrap handling, `ifOperStatus` transition events.

## 6. Metric naming & label schema (VictoriaMetrics)

Prometheus conventions: `netinv_<family>_<metric>_<unit>`. Examples:

```
netinv_if_in_octets_total{device_id="d_01H...", if_index="12", device="core-sw-1", site="dc-east", if_name="xe-0/0/1", if_alias="uplink-to-dc-west", vendor="juniper", speed_bps="10000000000"}
netinv_if_oper_status{...}            # 1 up / 2 down / ...
netinv_device_cpu_percent{device_id, cpu="0", device, site, vendor}
netinv_device_memory_used_bytes / _total_bytes
netinv_sensor_temperature_celsius{sensor="..."} · netinv_sensor_fan_rpm · netinv_sensor_psu_status
netinv_optic_rx_power_dbm / _tx_power_dbm{if_index, lane}
netinv_icmp_rtt_seconds{stat="min|avg|max"} · netinv_icmp_jitter_seconds · netinv_icmp_loss_ratio
netinv_poll_success{family="traffic|health|icmp"} · netinv_poll_duration_seconds
netinv_if_counters_repaired          # varbinds recovered by GET after a partial walk; 0 on a healthy agent (doc 10 §7)
netinv_device_uptime_seconds
netinv_firewall_session_count · netinv_firewall_session_max · netinv_firewall_session_setup_rate
```

`netinv_firewall_*` is device-level only (ADR-021): counts about the appliance, never tables about its policy. Session *utilization* is deliberately not a stored metric — it is `netinv_firewall_session_count / netinv_firewall_session_max`, so it cannot drift from the pair it is derived from, and platforms publishing no ceiling (FortiOS) simply return nothing rather than a fabricated denominator. `session_count` is always unlabelled: a per-protocol breakdown under the same name would put labelled and unlabelled series in the store together and double every session under `sum()`.

Cardinality rules (enforced in ingester): label values bounded (no timestamps/errors in labels); `device_id` + `if_index` are the identity; free-text (alias) sanitized and length-capped. Estimated series/device ≈ 15 + 12×interfaces → within NFR-03.

## 7. Query path & caching strategy

- **Raw graph queries:** UI → API `/metrics/query(_range)` → MetricsQL proxy to VM. The API injects scope guards (tenant label, doc 20) and enforces range/step limits; per-user query cache in Redis (15 s TTL) collapses duplicate NOC-wall queries.
- **Dashboard aggregates:** each panel's payload is computed once and shared by all viewers via Redis keys `dash:<panel>` (FR-DASH-01..08, NFR-12). v1 implements this cache-aside (compute on miss, 15 s TTL) — same shared-payload property with less machinery; the background leader-elected refresher variant is the upgrade path when viewer counts make even cache-miss recomputes contended.
- **Weathermap live data:** same pattern per map: `map:<id>:live` assembled every ≤30 s only while any client is subscribed (presence via Redis key with TTL heartbeat).
- **Inventory queries:** straight PG with indexes (doc 08); list responses cached briefly (5 s) keyed by filter hash — correctness over cleverness.

## 8. Event-driven synchronization

Domain events (JSON, CloudEvents-style envelope: `id`, `type`, `source`, `time`, `subject`, `data`, `trace_id`) on `events.domain`:

| Event | Producer | Consumers (v1) |
|---|---|---|
| `inventory.device.created/updated/retired` | API | ingester (label snapshot), alerter (scope refresh), audit |
| `inventory.changed` (field diffs) | sync service | audit/asset-history writer, notifier (info alerts opt-in) |
| `sync.completed/failed` | sync service | API (UI status), audit |
| `metrics.state.transition` (link/device up-down) | ingester | alerter (fast path — no VM polling needed for state alerts) |
| `alert.fired/acknowledged/resolved` | alerter/API | notifier, audit, dashboard cache invalidator |
| `poller.registered/heartbeat.missed` | API/scheduler | alerter (self-monitoring) |

Consumers are idempotent (event `id` dedupe, Redis SETNX 24 h). Events carry full payloads (fat events) so consumers don't call back for state — keeps services decoupled for future extraction.

## 9. Horizontal scaling model

- Stateless: API, ingester, notifier, poller (per site) — scale by replica count; RabbitMQ competing consumers distribute work.
- Singletons by election: scheduler, alerter, dashboard refresher (Redis lease, 15 s TTL, fencing token) — active/standby with <30 s failover.
- Data tier growth per NFR capacity triggers (vmcluster, PgBouncer, Redis Cluster).
- The 100k path changes **deployment shape only**: more poller replicas per site, more ingesters, vmcluster, sharded scheduler partitions — no code-architecture change (ADR-004).

## 10. Technology rationale summary

Each choice traces to an ADR: Go (001), VictoriaMetrics (002), PostgreSQL (003), RabbitMQ topology (012), scheduler-over-Temporal (013), compile-time connectors (014), modular monolith (017). If a future maintainer (human or AI) disagrees with any of these, the procedure is: new ADR proposing the change, impact scan of docs 05–19, then implement.
