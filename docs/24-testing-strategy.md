# 24 — Testing Strategy

**Status:** draft · **Depends on:** 13, 14, 10 · gates: 25

## 1. Pyramid & coverage bars (NFR-71)

```
        E2E (Playwright vs full compose stack)         ~30 journeys
      API/integration (testcontainers: PG,RMQ,VM)      per endpoint + pipeline
    component/contract (connector fixtures, differ)     heavy
  unit (domain, pure funcs)                              heaviest — ≥90% domain pkgs
```
Overall backend ≥70% lines; `domain/` + SyncDiffer + RuleEvaluator + DerivationService ≥90%; connectors ≥90% of mapping code. Coverage is a CI gate but reviewed for meaning, not gamed.

### 1.1 Where coverage actually stands (2026-08-10)

Those are targets, and the distance to them is large enough that stating it plainly is more useful than restating the goal:

| Scope | Target | Measured |
|---|---|---|
| Backend overall | ≥70% | **18.4%** |
| `connectors/` module | ≥90% of mapping code | 71.6% |
| `domain/` packages | ≥90% | 82.1% |
| SyncDiffer (`inventory/app/sync`) | ≥90% | 62.3% |
| RuleEvaluator (`alerting/app/evaluator`) | ≥90% | 51.8% |
| Rule validation (`alerting/app/rules`) | ≥90% | 90.2% |
| DerivationService | ≥90% | not implemented |

The shape is what one would expect from a codebase built outside-in against a live pilot: domain logic and connector mapping are reasonably covered, while adapters — HTTP handlers, Postgres stores, AMQP publishers, the six `cmd/` entrypoints — sit near zero and dominate the total.

The CI gate is therefore a **ratchet, not the 70% bar**: `scripts/coverage.sh` fails when coverage falls below `scripts/coverage-floor.txt`, currently 18.0% — raised four times as tests landed alongside features, each time as a deliberate commit. Gating at 70% today would produce a permanently red build, which teaches everyone to ignore CI and is worse than no gate. Raising the floor is a deliberate commit, so progress shows up in history instead of being asserted here. The 70% target stands; this records the honest starting point for closing it.

## 2. The SNMP problem — simulate, then verify on real iron

**Throwaway instances for the "real iron" half.** [Doc 33 §7](33-proxmox-lxc.md)
covers spinning up a complete NetInv in a Proxmox LXC in about ten minutes and
deleting it with one command, on any branch. That is the intended way to point
a change at real equipment without testing against the deployment people rely
on — which this project has already done the wrong way once, with integration
tests writing into a live database.


- **Recorded-walk fixtures:** connectors ship `testdata/*.snmpwalk` recorded from real devices (`scripts/record-fixture.sh`, which redacts serials, addresses and MACs at capture time — values *and* table indices, since tables like the Ruckus per-AP one are indexed by MAC). Four of seven have one today: `generic`/`cisco` from the simulator, `ruckus`/`ubiquiti` from real hardware; `huawei`/`juniper`/`zte` have no hardware to record from. Unit tests replay fixtures through a fake `Session` — connector tests need no network, run in ms, and lock in vendor behavior forever (a regression = a diff in normalized output).
- **Generated counters.** The demo profiles' octet counters are snmpsim `numeric` variation rather than fixed values, so `rate()` produces real throughput and a seeded fleet draws live graphs instead of flat lines at zero — which is what makes the dataset usable for screenshots (§5). Only the 64-bit `ifHCInOctets`/`ifHCOutOctets` are generated: the module computes `value = seconds_since_start × rate` and clamps at the type maximum, so a Counter32 at any realistic line rate pins to `0xffffffff` within minutes and stops moving. The generic connector prefers the HC counters anyway, with the 32-bit pair as fallback.
- **snmpsim** (`tools/snmpsim/`): serves those same walks over real UDP 161 for integration/E2E — the compose stack boots with 12 simulated devices (all 5 vendors + generic + edge cases: 32-bit-only counters, 4k-interface chassis, v3-authPriv, agent that times out intermittently, ifIndex-shifting device).
- **Real-hardware pass (Sprint 17):** the connector matrix (doc 10 §5) is validated against at least one physical unit per vendor; results recorded in each connector's README (model, OS, families verified). ZTE explicitly risk-flagged (R-07).

## 3. What each layer tests

**Unit (Go):** domain invariants (doc 15 §3 — each is a named test); SyncDiffer table-tests (identity resolution: reboot reindex, linecard swap, rename — doc 11 §3); counter-wrap & derivation math; RuleEvaluator transitions incl. flap logic; fingerprint stability; envelope crypto round-trip + wrong-key/nonce-reuse rejection; retry/breaker behavior (fake clock via `platform/clock`).

**Integration (testcontainers-go: PG + RabbitMQ + VictoriaMetrics + Redis):**
- Every API endpoint: happy path + authz matrix (every endpoint × 4 roles asserts doc 20 §5 exactly — generated test) + validation errors + idempotency-key replay.
- Pipeline: publish PollJob → fake poller batch → ingester → assert samples queryable in VM and state event emitted.
- Sync end-to-end: snapshot in → diff → asset_history rows + events out; missing-N state machine.
- Migrations: fresh-install and upgrade-from-previous both pass; expand-contract compliance check.
- **No-secret-leak invariant:** create credential/channel → grep every API response, log capture, event payload, and audit row for the secret material (NFR-41).

**Contract:** OpenAPI (`backend/api/openapi.yaml`) is generated in CI and diffed against committed — drift fails; frontend types generated from the same file — breaking change = frontend compile error. Doc-09-vs-OpenAPI drift is a checklist item on API PRs (human/AI gate, NFR-70).

**E2E (Playwright, `frontend/e2e/`):** the smoke suite runs in CI against a bare api (login lands on dashboard, generic-failure on bad password, weathermap list reachable, admin role-gated nav) plus data-dependent journeys gated on `NETINV_E2E_SEEDED=1` for local/staging against the demo fleet (inventory search/filter with URL state, device-detail deep-link → interface table). This suite already caught a real session-restore defect (a hard refresh logged users out, and StrictMode double-refresh tripped token-reuse revocation) — both fixed. It grows toward ~30 journeys (onboard→first-metrics, alert ack→graph, weathermap publish→live coloring, SMTP test-send via MailHog, audit trail) as staging with the full compose stack + snmpsim comes online.

**Frontend unit/component (Vitest+RTL):** formatters (bps/dBm/uptime), filter-grammar codec, permission gates, TimeSeries wrapper props, map editor store (undo/redo command stack).

## 4. Non-functional test suites (scheduled, not per-PR)

- **Load (`tools/loadgen`, nightly on staging):** simulate 500 devices (v1 assert: NFR-10/12/15/16/19) and 5k-device stretch profile monthly (early warning on capacity triggers). Loadgen publishes synthetic batches directly to `metrics.raw` (pipeline load) + k6 for API/dashboard concurrency (NFR-06).
- **Soak:** 72 h staging run before each release — memory/goroutine/PG-connection/series-churn flatness.
- **Chaos-lite (release candidates):** kill each singleton (leader failover <30 s), sever poller AMQP (buffer/drain per doc 07 §6), stop VM (degradation ladder per doc 23 §4), restart PG (recovery, no data loss for confirmed writes).
- **Security:** gitleaks, govulncheck/npm audit, trivy image scan (per-PR); testssl vs staging + authz-matrix suite (release); annual external pentest once commercial (doc 29).
- **Restore drill (monthly, and before each release):** `scripts/restore.sh` into scratch containers, verified by comparing row counts, metric series and sample values against the source at a timestamp inside the backup window — not by the script's exit code. The first real run (doc 20 §12.3) found a restore that silently recovered no metrics at all while exiting 0, so the assertion is on the data.

## 5. Test data & environments

Seeded demo dataset (`scripts/seed-demo.sh`): 2 sites, simulated devices, default rules — the same dataset drives local dev (NFR-73), E2E, screenshots in docs, and sales demos later. The README screenshots are taken from it.

**Known limitation:** `seed-demo.sh` addresses devices as `127.0.1.x`, which reaches the simulator only when the services run on the host (`make dev`). In the containerised deployment (doc 32/33) that address is the *poller's own* loopback, so ICMP succeeds — it pings itself — while SNMP has nothing listening, and devices sit in `pending` with graphs that never fill. It needs to resolve the simulator's address on the container network instead. Environments: `compose` (local/CI E2E) → `staging` (k8s, nightly load/soak) → `prod`. No test touches prod; staging data is synthetic only.

## 6. Definition of Done (every PR)

Code + tests at required tiers · lint/arch-boundaries clean · docs updated (NFR-70) · no new TODO without linked issue · CI green including coverage gates · for connector PRs: fixtures + zero-core-diff check (doc 10 §6).
