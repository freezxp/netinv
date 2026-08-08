# Mini-soak — v1.0.0-rc.1 (dev, 2026-08-08)

Full six-service stack against the 73-device simulated fleet, ~10 min continuous.
A representative shakeout, not the 72h staging soak (doc 24 §4, which needs a
persistent cluster).

## Result: clean

| Service | Goroutines (base → steady) | Heap steady |
|---|---|---|
| api | 17 → 18 | ~66 MB (plateau) |
| poller | 66 → 68 (134 transient during poll bursts) | ~1 MB |
| ingester | 16 → 18 | ~1.6 MB |
| scheduler | 14 → 14 | <1 MB |
| alerter | 14 → 16 | ~1.4 MB |
| notifier | 15 → 15 | <1 MB |

- No goroutine growth: poller flat at 68 across a 60s window (50-worker pool +
  infra); the 134 reading was a transient in-flight poll burst, not a leak.
- api heap plateaued (~66 MB) — no unbounded growth from the dashboard/query
  caches and label snapshots.
- Queues drained to zero; no ERROR lines after a test artifact was removed
  (a notification channel pointing at an already-stopped drill sink, which
  correctly retried 5× and gave up — the durable queue lost nothing).
- Silencing the sim's always-down eth1 across the load fleet stopped alert
  churn as designed.

## Follow-up
The 72h soak + full chaos matrix run on staging (persistent k8s) before v1.0.0
final, per doc 24 §4 and the doc 31 runbook.
