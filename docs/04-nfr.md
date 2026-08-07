# 04 — Non-Functional Requirements

**Status:** draft · **Depends on:** 02, DECISIONS.md (ADR-004)

Two columns matter throughout: **v1 target** (what we test and release against, ~500 devices) and **design ceiling** (what the architecture must reach without redesign, 100k devices). Verification method per doc 24 §load.

## 1. Scale & capacity

| ID | Requirement | v1 target | Design ceiling |
|---|---|---|---|
| NFR-01 | Managed devices | 500 | 100,000 |
| NFR-02 | Monitored interfaces | 25,000 (~50/device) | 5,000,000 |
| NFR-03 | Active time series | ~1M | ~150M (needs vmcluster) |
| NFR-04 | Sample ingest rate | ~2k samples/s | ~500k samples/s |
| NFR-05 | Sites / remote pollers | 5 sites, 1 poller each | 50 sites, 20 pollers/site |
| NFR-06 | Concurrent UI users | 25 | 500 |
| NFR-07 | Alert rules evaluated | 200 | 20,000 |

**Capacity triggers** (when to activate the next architectural gear — revisit at each threshold):
- \>5k devices → VictoriaMetrics single-node → `vmcluster`; PostgreSQL connection pooling via PgBouncer.
- \>10k devices → shard scheduler fan-out by site partition; dedicated ingester replicas per N sites.
- \>50k devices → split read API from write API deployments (CQRS seam, ADR-017); Redis Cluster.

## 2. Performance

| ID | Requirement | Target |
|---|---|---|
| NFR-10 | API read latency (inventory list, device detail) | p95 < 300 ms, p99 < 800 ms |
| NFR-11 | API write latency | p95 < 500 ms |
| NFR-12 | Dashboard full load (cached aggregates) | < 2 s to interactive |
| NFR-13 | Time-series graph query (24 h, 1 device) | p95 < 1 s |
| NFR-14 | Weathermap live-data refresh payload | < 200 ms server time, ≤ 30 s staleness |
| NFR-15 | Metric end-to-end latency (device counter → queryable) | < 15 s p95 |
| NFR-16 | Alert detection latency (condition true → alert firing) | ≤ evaluation interval + 30 s |
| NFR-17 | Notification latency (firing → delivered) | < 30 s p95 |
| NFR-18 | Inventory search at ceiling scale | < 1 s (FR-DEV-03) |
| NFR-19 | Poll cycle completion | 95% of scheduled polls complete within their interval; overrun polls are skipped-and-counted, never queued unboundedly |

## 3. Availability & reliability (v1 = single instance where noted; HA is roadmap)

| ID | Requirement | Target |
|---|---|---|
| NFR-20 | Platform availability (v1, excluding planned maintenance) | 99.5% monthly |
| NFR-21 | Metric collection continuity during core outage | Pollers buffer ≥ 15 min locally (FR-COLL-08) |
| NFR-22 | Data durability — metrics | No loss for acked queue messages; VM snapshots daily |
| NFR-23 | Data durability — PostgreSQL | RPO ≤ 24 h v1 (daily base + WAL archiving RPO ≤ 5 min from Sprint 19) |
| NFR-24 | Recovery time (full core restore from backup) | RTO ≤ 4 h, runbook-verified |
| NFR-25 | Graceful degradation | TSDB down → UI serves inventory/alerts with banner; PG down → collection continues buffering; queue down → pollers buffer |
| NFR-26 | Poller resilience | Poller restart resumes within 60 s with no manual action; missed cycles counted |

## 4. Data retention & storage

| Tier | Resolution | Retention | Est. size @500 dev / @100k dev |
|---|---|---|---|
| Raw | as collected (30–300 s) | 90 days | ~15 GB / ~3 TB |
| 5 m rollup (vmalert recording rules) | 5 min | 13 months | ~8 GB / ~1.6 TB |
| 1 h rollup | 1 h | 3 years | ~2 GB / ~400 GB |
| PostgreSQL (inventory+history+audit) | — | audit 12 mo online, asset history 24 mo | < 10 GB / ~300 GB |

- NFR-30: Retention tiers configurable per FR-SET-01; deletions are age-based, never count-based.
- NFR-31: Backup: nightly PG base backup + VM snapshot to off-cluster storage (NFS/S3-compatible, e.g. MinIO); weekly restore-test job in staging (doc 25).

## 5. Security (targets; design in doc 20)

- NFR-40: All network paths TLS 1.2+ (external) — UI/API, AMQP, SMTP; in-cluster plaintext acceptable v1 with mTLS as roadmap.
- NFR-41: Secrets never in logs, error messages, API responses, or metrics labels (tested, doc 24 §security).
- NFR-42: OWASP ASVS L2 alignment for the API; dependency and container CVE scanning gate in CI (doc 25).
- NFR-43: Session inactivity: refresh token idle-expiry 30 days; access token 15 min (FR-AUTH-02).
- NFR-44: Rate limits: 10 login attempts/min/IP; 600 API requests/min/token (burst 100); export 10/hour/user.

## 6. Operability

- NFR-50: Fresh install to first polled device < 30 min using Helm chart + quickstart doc (PRD G5).
- NFR-51: Zero-downtime upgrades for API/UI (rolling); scheduler/ingester allow < 60 s handover; DB migrations backward-compatible one version (expand-migrate-contract).
- NFR-52: All services expose `/healthz` (liveness), `/readyz` (readiness), and Prometheus `/metrics` (doc 22).
- NFR-53: Structured JSON logs with trace correlation (doc 21); log volume < 5 GB/day at v1 scale.
- NFR-54: All configuration via environment/ConfigMap; no config files edited in-container; every option documented in the chart's values.yaml.

## 7. Compatibility & portability

- NFR-60: Browsers: latest 2 versions of Chrome/Edge/Firefox/Safari; minimum viewport 1280×800 for full dashboard, responsive down to tablet (doc 30 §responsive); dark and light themes.
- NFR-61: Kubernetes 1.28+ conformant clusters; amd64 and arm64 images.
- NFR-62: SNMP device compatibility: any RFC 3411-compliant agent for generic collection; vendor connectors per doc 10 matrix.
- NFR-63: API stability: `/api/v1` is additive-only after v1.0; breaking changes require `/api/v2` (doc 09 §versioning).

## 8. Maintainability (AI-native)

- NFR-70: Docs-as-truth: any PR changing behavior must update the corresponding doc (CI check: PR template + reviewer rule).
- NFR-71: Backend test coverage ≥ 70% lines overall, ≥ 90% for domain packages (doc 24).
- NFR-72: A new connector must be addable with zero diffs outside `connectors/` (+1 registry import line) — enforced by review checklist (ADR-014).
- NFR-73: Every service starts locally via `docker compose up` with seeded demo data + SNMP simulator (doc 24 §sim) for AI/dev inner loop.
