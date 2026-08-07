# 01 — Executive Summary

**Status:** draft · **Depends on:** DECISIONS.md

## The product

NetInv is a self-hosted network asset monitoring platform that gives network operations a single pane of glass over multi-vendor infrastructure — Cisco, Juniper, Huawei, ZTE, and Ubiquiti at launch, extensible to any platform through a connector plugin framework. It collects device metrics over SNMP v2c/v3 and ICMP, stores them in a modern time-series database, and presents them through a purpose-built operations dashboard whose centerpiece is a live, editable **weathermap**: the network drawn as a map, nodes colored by health, links colored by real-time utilization.

## Why now

The tool this replaces — Cacti with the Weathermap plugin — defined a generation of NOC screens but is built on 20-year-old foundations: RRD files that discard resolution, PHP polling that collapses beyond a few thousand data sources, no API, no RBAC, no alerting workflow. Commercial alternatives (SolarWinds, PRTG) are expensive, Windows-centric, and closed. Modern open-source stacks (Prometheus + SNMP exporter + Grafana) are powerful but are toolkits, not products — they demand assembly and offer no inventory model, no discovery, no weathermap.

NetInv occupies the gap: **the Cacti experience, rebuilt on today's stack** — Go collectors, VictoriaMetrics storage, a React interface — designed from day one to grow from 500 to 100,000 devices and from a single organization to a commercial SaaS.

## Who it serves

| User | What they get |
|---|---|
| Network Engineer | Per-device drill-down, interface graphs, error/discard diagnostics, topology truth (LLDP/CDP) |
| Operations / NOC | The dashboard: status row, alert queue with ack workflow, weathermap, recent events |
| Service Assurance | Availability %, latency/loss trends, capacity watchlist, exportable evidence (CSV/Excel/PDF) |
| Cloud Administrator | API-first automation, Kubernetes-native deployment, audit trail |
| Management | Uptime and capacity reporting, inventory truth for budgeting and audits |

## Scope discipline

**v1 (20 sprints, solo developer + AI pair):** SNMP collection of interface traffic, device health, availability/latency, and inventory; alerting with Email/Webhook/Slack; asset inventory with search/filter/export and history; RBAC (Admin/Operator/Read-Only/Auditor); audit logging; the weathermap editor.

**Designed-in but deferred:** wireless controllers, firewall/LB metrics, NetFlow/sFlow, syslog/trap ingestion, Keycloak SSO, multi-tenancy, HA/DR. Each has an explicit seam in the architecture (doc 29) so activating it is an addition, not a rewrite.

## Architecture in one paragraph

Six Go services share one codebase: an **API** (REST, JWT, RBAC, CQRS read/write separation), a **scheduler** that fans polling jobs out over RabbitMQ, site-local **pollers** that execute SNMP/ICMP through vendor connector plugins and push metric batches home over TLS, an **ingester** that enriches and writes to VictoriaMetrics, an **alert engine** evaluating MetricsQL rules, and a **notifier**. PostgreSQL holds inventory, configuration, credentials (envelope-encrypted), alerts, and audit; Redis caches dashboard aggregates and enforces rate limits. Everything ships as containers on on-prem Kubernetes via Helm; remote datacenters run only a poller, connecting outbound — no inbound firewall holes.

## Investment & milestones

| Milestone | Sprint | Proof point |
|---|---|---|
| M1 Collection pipeline live | 6 | Real device metrics flowing into VictoriaMetrics end-to-end |
| M2 Alerting operational | 9 | Threshold breach → Slack in <60s, ack workflow |
| M3 Usable product | 14 | Dashboard + inventory + device detail in the browser |
| M4 Flagship complete | 16 | Weathermap editing + live utilization coloring |
| M5 v1.0 release | 20 | 5-vendor support, exports, hardening, pilot deployment across 4–5 sites |

## Strategic trajectory

v1 proves the product in the owner's own infrastructure. The dormant multi-tenant schema, OIDC-shaped auth, and per-site poller model are the on-ramp to an MSP offering and ultimately a commercial SaaS (doc 29). The connector framework is the moat: each added vendor/platform compounds the value of the single pane of glass.
