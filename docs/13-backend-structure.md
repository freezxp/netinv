# 13 — Backend Project Structure (Go)

**Status:** draft · **Depends on:** 05 (layering), 12, 16 (contexts)

One Go module `github.com/freezxp/netinv/backend`; six binaries from `cmd/`; bounded contexts under `internal/` with Clean Architecture layers inside each (doc 05 §1). Go 1.23+.

```
backend/
├── go.mod
├── cmd/                          # composition roots — wiring only, no logic
│   ├── api/main.go               # netinv-api
│   ├── scheduler/main.go         # netinv-scheduler
│   ├── poller/main.go            # netinv-poller (imports connectors/registry)
│   ├── ingester/main.go          # netinv-ingester
│   ├── alerter/main.go           # netinv-alerter
│   └── notifier/main.go          # netinv-notifier
├── internal/
│   ├── iam/                      # Identity & Access context
│   │   ├── domain/               #   User, Role, Token entities; policies
│   │   ├── app/                  #   commands/ queries/ ports.go
│   │   └── adapters/             #   postgres/ redis/ http/ (handlers)
│   ├── inventory/                # devices, interfaces, credentials, groups,
│   │   ├── domain/               # history, topology + sync service (doc 11)
│   │   ├── app/                  #   sync/ differ lives here (pure, heavily tested)
│   │   └── adapters/             #   postgres/ http/ events/ csv/
│   ├── collection/               # profiles, schedules, pollers, jobs
│   │   ├── domain/  app/  adapters/
│   │   └── pollerrt/             #   poller runtime: worker pool, SNMP session
│   │                             #   (gosnmp), ICMP prober, batcher, disk buffer
│   ├── metrics/                  # ingest pipeline + query proxy
│   │   ├── domain/               #   sample model, derivations, label rules
│   │   ├── app/                  #   ingest/ enrich/ query/ (range clamps)
│   │   └── adapters/             #   victoriametrics/ amqp/ http/
│   ├── alerting/                 # rules, evaluation, lifecycle, silences
│   ├── notify/                   # channels, policies, senders (smtp/slack/webhook)
│   ├── maps/                     # weathermap definitions, live-data assembler
│   ├── audit/                    # event consumer + append-only store + query API
│   ├── dashboard/                # aggregate refresher + panel queries (read-only)
│   └── platform/                 # shared kernel — the ONLY cross-context import:
│       ├── config/               #   env config loader (NFR-54)
│       ├── httpx/                #   router, middleware: auth, RBAC, ratelimit,
│       │                         #   idempotency, error envelope (doc 23)
│       ├── amqpx/                #   publisher confirms, consumer groups, DLQ
│       ├── pgx/                  #   pool, tx helper, migration runner (goose)
│       ├── redisx/               #   cache, locks, leases (fencing tokens)
│       ├── cryptox/              #   envelope encryption, KeyProvider iface (ADR-011)
│       ├── ulid/ clock/ logx/ tracex/ eventx/   # ids, time, logging, otel, event bus
│       └── authz/                #   permission constants + checker
├── migrations/                   # goose SQL, numbered, expand-migrate-contract
├── api/openapi.yaml              # generated from handlers; CI-checked vs doc 09
└── testdata/                     # cross-context fixtures (recorded walks live in connectors/)
```

## Rules that keep this clean (CI-enforced via go-arch-lint)

1. `domain/` imports **nothing** outside stdlib + its own context's domain.
2. `app/` imports its `domain/` + `platform/` interface types only; defines `ports.go` (repository/gateway interfaces) that `adapters/` implement.
3. `adapters/` never import another context's internals. Cross-context needs go through the other context's exported app interface (wired in `cmd/`) or events.
4. `cmd/` is the only place concrete adapters meet app services (constructor DI, ADR-001). Each main.go: load config → connect infra → wire → serve/consume → graceful shutdown (drain queues, finish in-flight polls).
5. Only `cmd/poller` imports `connectors/registry` — the API sees connectors solely via the catalog table (doc 08) and `connectors/sdk` types.
6. CQRS: `app/commands/*` mutate (pipeline: validate → authorize → execute in tx → audit → publish events); `app/queries/*` read (cache-aware). A handler is one file, one struct, one `Handle(ctx, cmd)` — greppable by AI.

## Cross-cutting choices

| Concern | Choice | Note |
|---|---|---|
| HTTP | chi router + std `net/http` | boring, middleware-friendly |
| DB access | pgx v5 + sqlc-generated queries | typed SQL, no ORM; repository pattern = sqlc behind `ports.go` interfaces |
| Migrations | goose | SQL files, embedded, run by API on start (leader-locked) |
| Queue | rabbitmq/amqp091-go via `platform/amqpx` | confirms + DLQ everywhere |
| SNMP | gosnmp | wrapped by `collection/pollerrt` session (doc 10 `Session`) |
| Validation | struct tags + hand validators in commands | errors → 422 details |
| Logging/tracing | slog JSON + OpenTelemetry | doc 21 |
| Tests | std testing + testcontainers-go (PG, RabbitMQ, VM) | doc 24 |

## Configuration (excerpt, full table in Helm values — doc 19)

`NETINV_PG_DSN`, `NETINV_REDIS_ADDR`, `NETINV_AMQP_URL`, `NETINV_VM_URL`, `NETINV_MASTER_KEY` (base64, ADR-011), `NETINV_JWT_SIGNING_KEY` (Ed25519 seed), `NETINV_SITE_ID` + `NETINV_ENROLL_TOKEN` (poller only), `NETINV_LOG_LEVEL`. Fail-fast on missing/invalid config at boot.
