# 22 — Monitoring Strategy (monitoring the monitor)

**Status:** draft · **Depends on:** 04, 05, 21

A monitoring product that silently fails is worse than none. NetInv monitors itself with its own stack: every service exposes Prometheus `/metrics` (NFR-52); a lightweight **vmagent** scrapes them into the same VictoriaMetrics (label `job=netinv-self`, isolated retention rules); **vmalert** evaluates self-alert rules; alerts route through NetInv's own notifier via a reserved internal webhook channel — with one deliberate exception below.

## 1. Golden signals per service

| Service | Key metrics (all prefixed `netinv_self_`) |
|---|---|
| api | http request rate/errors/duration (RED) by route class; auth failures; rate-limit hits; cache hit ratio; PG pool saturation |
| scheduler | tick duration; jobs published/cycle; **schedule lag** (now − next_due_at p95 — the "are we keeping up" number); leadership status |
| poller (per site) | polls ok/timeout/auth-fail rates; SNMP round-trip p95; worker pool saturation; batch publish confirm latency; **buffer depth & drops**; AMQP connection state |
| ingester | batches/s, samples/s; validation rejects; VM write latency/errors; enrichment-snapshot age |
| alerter | rules evaluated/cycle, eval duration, VM query errors, transitions emitted |
| notifier | deliveries by channel/outcome; retry depth; DLQ arrivals |
| queues (rabbitmq exporter) | depth, publish/deliver rates, DLQ depth per queue; consumer counts |
| data tier | PG (connections, replication lag, tx rate, table bloat), VM (active series, ingest rate, query latency, disk), Redis (memory, evictions, hit rate) |

## 2. SLOs (v1 — measured over 30 d, reviewed monthly)

| SLO | Target | Backing NFR |
|---|---|---|
| Poll completion: scheduled polls executed within interval | 99% | NFR-19 |
| Ingest freshness: sample age at write < 15 s p95 | 99% | NFR-15 |
| Alert latency: condition → firing within eval+30 s | 99% | NFR-16 |
| API availability (5xx ratio < 0.1%) + p95 < 300 ms reads | 99.5% | NFR-10/20 |
| Notification delivery success (excl. dead endpoints) | 99% | NFR-17 |

Error budgets gate feature-vs-hardening choices in sprint planning (doc 27).

## 3. Self-alert rules (shipped enabled)

Critical: any service target down >2 min · schedule lag p95 > poll interval · `metrics.raw` or any DLQ growing >5 min · poller heartbeat missed (per site) · VM/PG/Redis/RabbitMQ down or disk >85% · ingest freshness SLO burn >14×.
Warning: `netinv_if_counters_repaired > 0` sustained (an agent whose walk is broken; NetInv is compensating with GETs and the device wants restarting — doc 10 §7) · a site job queue with more than one consumer, or unacked messages with nothing ready (a leaked AMQP consumer — doc 07 §6.1) · repeating `sync result requeued` for one device (a sync that cannot apply, freezing that device's inventory — doc 11 §3.1) · poller buffer non-empty >5 min · validation rejects >1% · notifier retries elevated · PG replication lag (once replica exists) · certificate expiry <21 d · backup job missed/failed · enrichment snapshot stale >10 min.

## 4. The dead-man's switch (who watches the watcher)

Two paths independent of NetInv's own pipeline: (1) vmalert "always-firing" heartbeat rule → external healthchecks.io-style endpoint (or ops team's existing system) — silence = the stack is down; (2) the notifier's internal channel also mirrors critical self-alerts directly to the ops email via SMTP, bypassing the alerting DB path. This is the one place we deliberately double-send.

## 5. Dashboards (custom UI, ADR-009)

An **Admin → System Health** page (doc 30 §12) renders the self-metrics: pipeline throughput (jobs → samples → writes), per-site poller health, queue depths, SLO burn-down, data-tier vitals. Same uPlot components as the product graphs — self-monitoring is a first-class product feature (it's also what we demo to prove the platform scales).

## 6. Capacity watch on ourselves

Monthly review checklist: VM active series & churn vs NFR-03; PG largest tables vs partition plan; queue peak depths; poller worker saturation per site; then act per NFR §1 capacity triggers. The review is a recurring item in sprint planning — capacity surprises are the #1 monitoring-platform killer (risk R-04).
