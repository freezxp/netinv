# 18 — Deployment Diagram

**Status:** draft · **Depends on:** 05, 06 · k8s detail: 19

## Physical topology (v1: core site + 3–4 remote sites)

```mermaid
flowchart TB
    subgraph coredc["Core DC (dc-core) — on-prem Kubernetes cluster"]
        subgraph ns["namespace: netinv"]
            ING["Ingress (nginx)<br/>TLS 1.2+ · :443"]
            APIS["netinv-api ×2"]
            SCH["netinv-scheduler ×2 (1 leader)"]
            ALR["netinv-alerter ×2 (1 leader)"]
            IGS["netinv-ingester ×2"]
            NTF["netinv-notifier ×1"]
            PLC["netinv-poller (core site) ×1"]
            FE["frontend (static, served by ingress/nginx pod)"]
        end
        subgraph nsdata["namespace: netinv-data"]
            PGC[("PostgreSQL 16<br/>CloudNativePG, 1 primary +<br/>WAL archive → backup store")]
            VMS[("VictoriaMetrics single-node<br/>PVC, daily snapshot")]
            RED[("Redis ×1")]
            RMQ{{"RabbitMQ ×1 (v1)<br/>AMQPS :5671 exposed via LB"}}
        end
        BK[("Backup store<br/>MinIO / NFS off-cluster")]
    end

    subgraph site2["Remote DC (dc-2..5) — each"]
        RP["netinv-poller ×1<br/>(k8s Deployment or docker-compose)<br/>+ 2GB disk buffer PVC"]
        DEV2["Network devices<br/>SNMP/161 · ICMP (device mgmt VLAN)"]
        RP -->|"SNMP v2c/v3 UDP:161<br/>ICMP · from poller IP only"| DEV2
    end

    USERS["Browsers / API clients"] -->|HTTPS :443| ING
    ING --> APIS & FE
    RP -->|"AMQPS :5671 outbound only<br/>(TLS, per-site vhost+user)"| RMQ
    PLC --> RMQ
    PGC --> BK
    VMS --> BK

    NTF -->|SMTP 587 STARTTLS / HTTPS| EXTN["SMTP relay · Slack · webhooks"]
```

## Network paths & firewall matrix

| From | To | Proto/Port | Notes |
|---|---|---|---|
| Users | Ingress | TCP 443 | only user-facing entry |
| Remote poller | Core RabbitMQ LB | TCP 5671 (AMQPS) | **only** core-bound port from sites; per-site vhost credentials (doc 20 §8) |
| Poller | Devices | UDP 161, ICMP | source-restrict SNMP ACLs on devices to poller IPs |
| Notifier | SMTP/Slack/webhooks | 587/443 egress | allowlist egress if policy requires |
| CI (GitHub) | — | none inbound | cluster **pulls** images from GHCR (doc 25); no GitHub→on-prem path |
| Admin | k8s API | 6443 | platform ops only, not app traffic |

Remote sites need zero inbound rules (ADR-006). If a site's WAN dies, its poller buffers ≥15 min and device SNMP data survives the gap (doc 07 §6); core keeps serving other sites.

## Sizing (v1, 500 devices — revisit at NFR capacity triggers)

| Component | Replicas | CPU req/lim | Mem req/lim | Storage |
|---|---|---|---|---|
| api | 2 | 250m/1 | 256Mi/512Mi | — |
| scheduler / alerter | 2 each | 100m/500m | 128Mi/256Mi | — |
| ingester | 2 | 250m/1 | 256Mi/512Mi | — |
| notifier | 1 | 50m/250m | 64Mi/128Mi | — |
| poller (per site) | 1 | 250m/1 | 256Mi/512Mi | 2Gi buffer PVC |
| PostgreSQL | 1 (+replica S19) | 1/2 | 2Gi/4Gi | 50Gi |
| VictoriaMetrics | 1 | 1/2 | 2Gi/4Gi | 100Gi (doc 04 §4) |
| Redis | 1 | 100m/500m | 256Mi/512Mi | — (cache-only, no persistence needed) |
| RabbitMQ | 1 → 3 (HA phase) | 500m/1 | 512Mi/1Gi | 10Gi |

Whole v1 core fits in ~8 vCPU / 16 GiB — one modest node pool; remote poller runs on anything that runs a container.
