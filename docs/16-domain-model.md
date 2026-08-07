# 16 — Domain Model (DDD)

**Status:** draft · **Depends on:** 05 · **Constrains:** 13, 15, 17

## 1. Ubiquitous language (the vocabulary — use these words everywhere: code, docs, UI, commits)

| Term | Meaning | Not to be confused with |
|---|---|---|
| **Device** | A managed network element (switch/router/AP) we poll | "node" (weathermap term), "host" (future server monitoring) |
| **Interface** | A port/logical interface on a device, keyed by ifIndex | "link" (a map/topology edge) |
| **Site** | A physical location (datacenter) owning devices and pollers | k8s cluster/namespace |
| **Poller** | A site-local collection agent process | "collector" (a connector capability) |
| **Connector** | Vendor driver translating SNMP into normalized data | poller (runtime that hosts connectors) |
| **Collector** | One capability of a connector (interface/health/topology…) | — |
| **Poll** | One execution of one metric family against one device | sync |
| **Sync** | Structural inventory reconciliation (doc 11) | poll (metrics) |
| **Sample** | One (metric, labels, timestamp, value) fact | metric (the named series) |
| **Family** | Poll job category: traffic · health · icmp · sync | metric name |
| **Adjacency** | LLDP/CDP-reported neighborship | map link (drawn by operator) |
| **Map / Node / Link** | Weathermap drawing objects referencing inventory | topology (network truth) |
| **Alert Rule / Alert (instance)** | Condition config / a firing fact | notification (a delivery) |
| **Silence** | Scoped, expiring notification mute | ack (human took ownership) |
| **Asset history** | Append-only record of detected inventory change | audit (record of human/system *actions*) |

## 2. Bounded contexts & relationships

Eight contexts (map in doc 06 §2): **Identity & Access**, **Inventory** (includes sync + topology), **Collection** (profiles, schedules, pollers, jobs), **Metrics** (ingest + query), **Alerting**, **Notification**, **Maps**, **Audit**. Context-mapping patterns:

- Inventory is **upstream supplier** to Collection, Metrics, Alerting, Maps (they conform to its device/interface identity).
- Metrics ↔ Alerting: **open-host service** — Alerting consumes the query API + `state.transition` events, no shared internals.
- Notification is **downstream** of Alerting via published events only (could be a separate product).
- Audit is a **conformist observer**: consumes everyone's events, exposes nothing back.
- `connectors/sdk` is a **published language** between core and vendor plugins (doc 10).

## 3. Aggregates (consistency boundaries)

| Aggregate root | Members | Key invariants (doc 15 §3) | Transaction rule |
|---|---|---|---|
| **Device** | Interfaces, Components | one credential/connector/profile/site; operator-vs-network field ownership | one sync diff = one transaction on one Device |
| **Credential** | — | write-only secret; ref-count guards deletion | |
| **PollingProfile** | — | valid intervals (min 10 s) | fan-out to schedules is eventually consistent (next tick) |
| **Map** | Revisions, link bindings | published rev immutable; endpoint bindings resolvable at publish | publish = validate + extract `map_links` atomically |
| **AlertRule** | — | expr parses; scope resolvable | |
| **AlertInstance** | AlertEvents | single live instance per (rule, fingerprint); legal state transitions only | lifecycle transitions are CAS on state |
| **User** | tokens | lockout policy; role grants audited | |
| **Site** | Pollers | poller belongs to exactly one site | |
| **Silence / Channel / Policy** | — | — | |

Aggregates reference each other **by ID only**. Cross-aggregate reactions ride domain events (doc 05 §8) — e.g., Device retired → Collection removes schedules (event consumer), never a cross-aggregate transaction.

## 4. Domain services (logic that owns no single aggregate)

- **SyncDiffer** (Inventory): snapshot × current state → change set + identity resolution (doc 11 §3). Pure function; the most-tested code in the system.
- **SchedulePlanner** (Collection): profiles × devices → jittered `next_due_at` plan.
- **DerivationService** (Metrics): counter deltas → rates/utilization; wrap handling.
- **RuleEvaluator** (Alerting): rule + query results → fire/resolve decisions + fingerprints; flap detector.
- **RoutingService** (Notification): alert × policies → channel dispatch set.
- **ConnectorMatcher** (Collection): sysObjectID → scored connector candidates (doc 11 §7).

## 5. Domain events (canonical list — envelope in doc 05 §8)

`inventory.device.created|updated|retired` · `inventory.changed` · `inventory.interface.reindexed` · `sync.requested|completed|failed` · `metrics.state.transition` · `alert.fired|acknowledged|resolved|flapping` · `silence.created|expired` · `notification.delivered|failed` · `poller.registered|heartbeat.missed|recovered` · `auth.login.success|failure|lockout` · `config.changed` (generic, carries resource + diff → audit).

Naming: `context.subject.pastTenseVerb`. Events are facts — consumers may not veto. Anything needing veto is a command (API call), not an event.

## 6. What is deliberately *not* modeled

- Samples/series (facts in VM, not domain objects — doc 15 §4).
- Vendor quirks (connector-internal; the domain sees only normalized values).
- Tenancy behavior (ID plumbing only, ADR-005).
- Workflow/approval chains beyond discovery-approve (keep v1 lean; revisit with SaaS, doc 29).
