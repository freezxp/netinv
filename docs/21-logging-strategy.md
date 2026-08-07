# 21 — Logging Strategy

**Status:** draft · **Depends on:** 05, 13 (`platform/logx`)

## 1. Format & transport

Structured JSON to stdout (12-factor; k8s collects). One line per event via Go `slog`. Cluster-side pipeline (ops choice, chart-documented): Promtail/Alloy → **Loki** (recommended — pairs with VM ecosystem) or any JSON-capable collector. NetInv never writes log files.

**Canonical fields (every line):** `ts` (RFC3339Nano) · `level` · `svc` (netinv-api…) · `ver` · `msg` · `trace_id`/`span_id` (when in a traced flow) · plus bounded context fields: `device_id`, `site_id`, `poller_id`, `job_id`, `rule_id`, `user_id` (ID only, never username in logs), `http.method/route/status/dur_ms`, `amqp.queue`, `err` (chain string), `err_code` (doc 23 taxonomy).

## 2. Levels & discipline

| Level | Use | Examples |
|---|---|---|
| `error` | unexpected failure needing human/agent attention; always with `err` + context | VM write failed after retries; DLQ delivery; panic recovered |
| `warn` | degraded-but-handled; expected-failure of external things | device SNMP timeout (rate-limited log), poller reconnect, cache miss storm |
| `info` | lifecycle & business facts, low volume | service start/stop with config summary (secrets redacted), migration applied, poller registered, alert fired |
| `debug` | flow detail, off in prod by default; per-service toggle via env without restart (SIGHUP/config reload) | per-job timings, differ decisions, query text |

Rules: no logging in hot per-sample paths (that's what metrics are for — doc 22); per-device failure logs are rate-limited (first + every 10th + summary) so a dead site can't flood (NFR-53 < 5 GB/day); never log secrets/payload bodies — redaction middleware as backstop (doc 20 §10); every `error` must be actionable — if nobody would act, it's `warn`.

## 3. Correlation

`trace_id` is born at the API edge (or scheduler tick / queue publish) and propagates: HTTP middleware → context → AMQP headers → consumer context → child spans. The same ID appears in API response envelopes (`error.trace_id`, doc 23) and audit rows (doc 08) — one ID walks UI error → log line → trace → audit. OpenTelemetry SDK wired in `platform/tracex`; OTLP exporter off by default v1 (flip on when a collector exists), logs alone still carry the IDs.

## 4. What each service must log (minimum contract)

- **api:** one line/request at `info` (route, status, dur, user_id) — sampled to 10% for 2xx reads at high volume; all 4xx/5xx unsampled.
- **scheduler:** per tick at `debug`; per-cycle summary at `info` (jobs published, lag); leadership changes `info`.
- **poller:** batch summaries (`info`, N samples, M devices, failures count), per-device failures rate-limited `warn`, buffer state transitions `warn/info`.
- **ingester:** batch results `debug`, validation rejects `warn` (with reason, no payload), VM retry exhaustion `error`.
- **alerter:** evaluation cycle summary `debug`, every alert state transition `info`.
- **notifier:** every delivery attempt outcome `info`/`warn`/`error` (no message body).

## 5. Retention & access

Log retention (Loki): 30 d hot; `error` streams 90 d. Logs are ops data — RBAC'd to platform operators, not exposed in the product UI (the product's audit/event features come from PG, not logs). Local dev: pretty console handler (`NETINV_LOG_PRETTY=1`).
