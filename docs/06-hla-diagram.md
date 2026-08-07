# 06 — High-Level Architecture Diagram

**Status:** draft · **Depends on:** 05

## System overview

```mermaid
flowchart TB
    subgraph users["Users"]
        UI["React SPA<br/>(dashboard · weathermap · inventory)"]
        AUTO["Automation / scripts<br/>(API tokens)"]
    end

    subgraph core["Core site — Kubernetes"]
        subgraph control["Control plane"]
            API["netinv-api<br/>REST · JWT/RBAC · CQRS handlers<br/>sync service · dashboard refresher"]
            SCHED["netinv-scheduler<br/>poll/sync job fan-out<br/>(leader-elected)"]
            ALERT["netinv-alerter<br/>MetricsQL rule evaluation<br/>(leader-elected)"]
            NOTIF["netinv-notifier<br/>email · webhook · slack"]
        end
        subgraph data["Data plane"]
            INGEST["netinv-ingester<br/>validate · enrich · derive"]
        end
        subgraph stores["State"]
            PG[("PostgreSQL 16<br/>inventory · config · alerts<br/>credentials · audit")]
            VM[("VictoriaMetrics<br/>time series")]
            REDIS[("Redis<br/>caches · locks · leases<br/>rate limits")]
            MQ{{"RabbitMQ<br/>jobs · metrics · events · notify"}}
        end
        LOCALPOLL["netinv-poller (core site)"]
    end

    subgraph site2["Remote site × 3–4"]
        RPOLL["netinv-poller<br/>(outbound AMQPS only,<br/>local overflow buffer)"]
        RDEV["Network devices<br/>Cisco · Juniper · Huawei<br/>ZTE · Ubiquiti"]
    end
    CDEV["Core-site network devices"]

    EXT["SMTP · Slack · Webhook targets"]

    UI -->|HTTPS REST| API
    AUTO -->|HTTPS REST| API
    API --> PG
    API --> REDIS
    API -->|MetricsQL proxy| VM
    API <-->|events| MQ

    SCHED -->|read schedules| PG
    SCHED -->|publish jobs| MQ
    SCHED --> REDIS

    MQ -->|"poll.site.*"| LOCALPOLL
    MQ -->|"poll.site.* (AMQPS 5671)"| RPOLL
    LOCALPOLL -->|SNMP/ICMP| CDEV
    RPOLL -->|SNMP v2c/v3 + ICMP| RDEV
    LOCALPOLL -->|metric batches| MQ
    RPOLL -->|metric batches| MQ

    MQ -->|metrics.raw| INGEST
    INGEST -->|write| VM
    INGEST -->|state events| MQ

    ALERT -->|query| VM
    ALERT --> PG
    ALERT <-->|"alert.* events"| MQ
    MQ -->|notify.dispatch| NOTIF
    NOTIF --> EXT
    NOTIF -->|delivery log| PG
```

## Reading the diagram

- **Left-to-right trust boundary:** users touch only the API over HTTPS; devices are touched only by pollers; nothing at a remote site accepts inbound connections (ADR-006).
- **Single writers:** PostgreSQL config/inventory written only by the API; VictoriaMetrics written only by the ingester. Everything else reads or goes through events — this is what keeps the modular monolith extractable (doc 05 §3).
- **RabbitMQ is the spine:** four logical flows (jobs out, metrics in, domain events around, notifications out) detailed in doc 05 §4.
- **Leader-elected singletons** (scheduler, alerter) are drawn once but deploy as ≥2 replicas with a Redis lease (doc 05 §9).

## Bounded-context view

```mermaid
flowchart LR
    subgraph IAM["Identity & Access"]
        A1[users · roles · tokens · sessions]
    end
    subgraph INV["Inventory"]
        B1[sites · devices · interfaces<br/>credentials · groups · history · topology]
    end
    subgraph COL["Collection"]
        C1[polling profiles · schedules<br/>pollers · connectors · jobs]
    end
    subgraph MET["Metrics"]
        D1[series · rollups · query]
    end
    subgraph ALR["Alerting"]
        E1[rules · alerts · silences]
    end
    subgraph NOT["Notification"]
        F1[channels · policies · deliveries]
    end
    subgraph MAPC["Topology & Maps"]
        G1[maps · nodes · links · LLDP adjacency]
    end
    subgraph AUD["Audit"]
        H1[audit events - append only]
    end

    COL -->|polls per| INV
    MET -->|identified by| INV
    ALR -->|scoped by| INV
    ALR -->|queries| MET
    NOT -->|routes| ALR
    MAPC -->|references| INV
    MAPC -->|colored by| MET
    AUD -.->|observes all via events| IAM & INV & COL & ALR & NOT
```

Context ownership, aggregates, and language: doc 16. Package enforcement: doc 13.
