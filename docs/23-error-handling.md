# 23 — Error Handling Strategy

**Status:** draft · **Depends on:** 05, 13, 21

## 1. Taxonomy

Every error in the backend is classified at its origin into one of five kinds (Go: `platform/errx` sentinel wrapping; the kind travels up the chain):

| Kind | Meaning | Default handling |
|---|---|---|
| `invalid` | caller's fault (validation, bad state transition) | no retry; 4xx at API; never alerts |
| `unauthorized` / `forbidden` | authn/authz | 401/403; audit; counts toward abuse metrics |
| `not_found` | absent resource | 404; no retry |
| `conflict` | concurrency/duplicate (CAS failure, unique violation, stale map rev) | 409; client retries with fresh state |
| `transient` | timeouts, connection loss, 5xx from deps, queue unavailable | **retry with backoff**; escalate to `dependency_down` after budget |
| `internal` | bugs, invariants broken, panics | no retry; 500; `error` log + self-metric; recover middleware keeps process alive |

Wrapping rule: add context at every boundary (`fmt.Errorf("sync device %s: %w", id, err)`), classify once at origin, never reclassify upward, never swallow — either handle or return, log at the top only (no double logging).

## 2. Retry policies (uniform helpers in `platform/retryx`)

| Path | Policy |
|---|---|
| SNMP poll | in-poll: timeout 5 s × 2 retries (profile). Cross-cycle: no immediate retry — next schedule slot (NFR-19); consecutive-failure count drives `unreachable` state (3) and scheduler backoff (2× interval, cap 24 h — doc 11 §6) |
| VM writes (ingester) | exp backoff 250 ms→30 s, jitter, ∞ (batch stays unacked; queue is the buffer); circuit breaker after 5 consecutive → pause consumer, `dependency_down` alert |
| PG (API commands) | single retry on serialization/connection errors only; otherwise surface |
| AMQP publish | publisher confirms; unconfirmed → reconnect + republish (idempotent consumers absorb dupes, doc 05 §8) |
| Notifications | 5 attempts, 30 s→16 min backoff → DLQ + `deliveries.status=failed` + self-alert (FR-NOT-04) |
| HTTP calls to Slack/webhooks | timeout 10 s; 4xx = permanent (no retry, mark misconfigured), 5xx/timeout = transient |

Circuit breakers (`platform/retryx.Breaker`) guard each external dependency per service; half-open probes every 30 s; state exposed as self-metric (doc 22).

## 3. Queue poison handling

Consumer error → classify: `transient` → nack+requeue with per-message redelivery cap (x-delivery-count, 5) → DLQ; `invalid`/`internal` (malformed payload, handler bug) → straight to DLQ with reason header. DLQs are monitored (doc 22 §3); an admin CLI (`scripts/dlq-replay`) supports inspect/replay/purge after fixes. Nothing loops forever; nothing is silently dropped.

## 4. Degradation ladder (NFR-25)

| Dependency down | Behavior |
|---|---|
| VictoriaMetrics | UI: inventory/alerts served, graphs show "metrics unavailable" banner; ingester holds batches in queue; alerter pauses (no false resolves — alerts freeze with `stale` marker) |
| PostgreSQL | API read-only from caches where possible → 503 for writes; collection pipeline continues (schedules already in flight); ingester still writes VM (enrichment from last snapshot) |
| Redis | caches bypass (slower, correct); rate limiting fails-open with warn; leader leases fall back to "hold last role, no takeover" (fencing prevents split-brain) |
| RabbitMQ | pollers: local schedule continuation + disk buffer (doc 07 §6); API queues events in outbox table, drains on recovery |
| SMTP/Slack | retries → DLQ; alerts remain visible in UI (the UI is the source of truth, notifications are a convenience) |

## 5. API error envelope (FR-API-04)

```json
{"error": {
  "code": "validation_failed",          // machine: snake_case, stable, documented
  "message": "traffic_interval_s must be ≥ 10",   // human, safe to display
  "details": [{"field": "traffic_interval_s", "rule": "min", "min": 10}],
  "trace_id": "7f3a…"                   // joins logs/audit (doc 21 §3)
}}
```

Codes are an enum in one Go file + doc 09 appendix; 5xx bodies never contain internals (no stack traces, no SQL, no dep hostnames) — details live in logs keyed by `trace_id`. Frontend maps codes → friendly messages + field highlighting; unknown codes fall back to generic + trace ID shown for support.

## 6. Frontend error handling

Route-level error boundaries (app keeps shell alive); TanStack Query: stale-while-revalidate + retry (2, transient only); global handler: 401 → silent refresh → login; 403 → permission toast; 429 → backoff banner; network-down → offline banner with auto-retry. Live panels show data age when refresh fails ("as of 09:12:30") rather than blanking — a NOC wall must degrade honestly.

## 7. Process-level

Panic recovery middleware in every entrypoint (HTTP, consumer, scheduler loop): log with stack at `error`, increment self-metric, fail the single unit of work, keep serving. Graceful shutdown everywhere: stop intake → drain in-flight (≤60 s) → close infra. Startup is fail-fast on config, but **dependency-patient** (waits with backoff for PG/AMQP — k8s ordering shouldn't crash-loop the app).
