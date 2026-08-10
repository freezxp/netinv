# NetInv — Network Asset Monitoring Platform

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](backend/go.mod)
[![React](https://img.shields.io/badge/React-18_%2B_TypeScript-61DAFB?logo=react&logoColor=white)](frontend/package.json)
[![Status: pilot](https://img.shields.io/badge/status-pilot-orange.svg)](#)

Centralized, vendor-neutral network monitoring: SNMP (v2c/v3) collection from Cisco, Juniper, Huawei, ZTE, Ubiquiti and Ruckus devices into a modern time-series stack, with a live topology **weathermap** as the flagship view. The spiritual successor to Cacti + Weathermap, rebuilt on VictoriaMetrics instead of MRTG/RRD.

> **Status: pilot — all 20 sprints complete, running live.** Milestones M1 (collection pipeline), M2 (alerting), M3 (usable product) and M4 (weathermap flagship) are achieved, and the platform now monitors a real network: four UniFi gateways in an SD-WAN mesh plus a Ruckus Unleashed estate, with a live weathermap over the WireGuard tunnels between them.
>
> The `generic`, `ubiquiti` and `ruckus` connectors are validated against real hardware and documented in [doc 10](docs/10-connector-architecture.md) with what each device actually exposes — including what it does not. **`cisco-ios`, `juniper-junos`, `huawei-vrp` and `zte-zxr` are written against MIB specifications and recorded fixtures but have not yet met real units.** Also outstanding before v1.0: a 72 h soak and chaos run on staging, the security checklist against TLS, and a backup/restore drill. Sprint log: `git log --oneline`.

## Try it in one command

Docker + Compose v2 is all you need — no Go, Node, or database to install:

```bash
git clone https://github.com/freezxp/netinv.git && cd netinv
./deploy/compose-app/quickstart.sh
```

It generates secrets, builds the images, starts the whole platform (six
services + UI + a bundled data tier and SNMP simulator), and prints your login.
Open **http://localhost:8090**. Full guide: [docs/32-quickstart.md](docs/32-quickstart.md).

## What it does (v1)

- **Collects** interface traffic, errors/discards, device health (CPU/memory/temp/PSU/optics), ICMP availability/latency, and inventory metadata from network devices over SNMP v2c/v3.
- **Stores** metrics in VictoriaMetrics for **2 years by default** (`NETINV_VM_RETENTION`), inventory/config/audit in PostgreSQL. A capacity view reports what the disk actually sustains against what retention asks for, measured rather than estimated.
- **Shows** a single dashboard: status summary, inbound and outbound bandwidth per site, active alerts, Top-N lists, capacity watchlist, and an editable utilization-colored weathermap. Every chart shares one time range — **Cacti's graph timespans**, from Half Hour to 2 Years.
- **Tunes** collection cadence fleet-wide from the UI (1/5/10/15 minutes), with query resolution and `rate()` windows following automatically.
- **Alerts** via Email, Webhook, and Slack with severity-based routing, ack/silence workflow.
- **Scales** from a single site to 100k devices across multiple datacenters via site-local pollers phoning home over RabbitMQ.

## Stack (decided — see [DECISIONS.md](DECISIONS.md))

| Layer | Choice |
|---|---|
| Backend | Go (modular monolith, microservice-ready) |
| Frontend | React + TypeScript + Vite, custom visualization (no Grafana) |
| Metrics | VictoriaMetrics (PromQL/MetricsQL) |
| Relational | PostgreSQL 16 |
| Cache | Redis |
| Queue | RabbitMQ |
| Auth | Local accounts + JWT (OIDC-shaped; Keycloak later) |
| Deploy | Docker → on-prem Kubernetes, Helm |
| CI/CD | GitHub Actions → GHCR |

## Repository map

```
netinv/
├── README.md          ← you are here
├── CONTRIBUTING.md    ← how to work in this repo; what help is most wanted
├── SECURITY.md        ← reporting vulnerabilities; what NetInv holds worth attacking
├── CLAUDE.md          ← AI onboarding: read this first if you are an AI agent
├── DECISIONS.md       ← architecture decision log (ADR-lite); the "why" behind everything
├── docs/              ← the 33-document design package (numbered, see docs/README.md)
├── backend/           ← Go services: api, scheduler, poller, ingester, alerter, notifier
├── connectors/        ← vendor connector packages (cisco, juniper, huawei, zte, ubiquiti, ruckus)
├── frontend/          ← React + TypeScript app
├── deploy/            ← Helm charts, k8s manifests, docker-compose
└── scripts/           ← dev and CI helpers (seed-demo, licences, connector contract)
```

## Reading order

1. [docs/01-executive-summary.md](docs/01-executive-summary.md) — 5-minute overview
2. [docs/05-system-architecture.md](docs/05-system-architecture.md) — how it works
3. [docs/26-development-roadmap.md](docs/26-development-roadmap.md) — what gets built when
4. Full index: [docs/README.md](docs/README.md)

## Contributing

Outside eyes are worth more than outside code right now — especially if you own
hardware NetInv has never been tested against. Four of the seven connectors
have only ever met MIB specifications, and every connector that *has* met real
hardware needed corrections that reading the spec would never have found.

If you point NetInv at a device, [tell us what happened](../../issues/new?template=hardware_validation.yml)
— including when it all worked. Start with [CONTRIBUTING.md](CONTRIBUTING.md).

## Project context

Solo developer + AI pair (Claude). Launch target under 500 devices across 4–5 on-prem datacenters, single organization, self-hosted; multi-tenancy and SaaS are designed-in but dormant. 20 two-week sprints, backend first.

## Licence

[Apache License 2.0](LICENSE) — commercial use, modification and distribution
permitted, with an explicit patent grant (ADR-019). Every shipped dependency is
permissively licensed; `make licenses` verifies that and fails on copyleft.
