# 28 — Risk Assessment

**Status:** draft · **Depends on:** 26, 27 · Review cadence: each phase gate (doc 26)

Scoring: Likelihood × Impact, 1–5 each. **Exposure = L×I** (≥12 red, 6–11 amber, ≤5 green). Owner is "dev" (solo) unless noted; the mitigation column is what's *already designed in*, contingency is the fallback if it fires anyway.

| ID | Risk | L | I | Exp | Mitigation (designed-in) | Contingency |
|---|---|---|---|---|---|---|
| **R-01** | **Solo-dev bus factor / burnout** — one person carries 20 sprints | 3 | 5 | **15** | AI-native repo: any agent/contractor resumes from docs (CLAUDE.md, NFR-70); 30% sprint slack; phases end demoable (morale) | Freeze scope at last green milestone — M2/M3 are each independently useful products |
| **R-02** | **Scope creep** — 12 metric categories + SaaS dreams vs 10 months | 4 | 4 | **16** | ADR-007 hard scope cut; P1/P2 gates in PRD; backlog discipline (27); DECISIONS.md records every change | Cut P1 items (discovery UI, PDF, events stream) — release criteria touch P0 only |
| **R-03** | Weathermap editor UX harder than estimated (flagship) | 3 | 4 | 12 | React Flow carries pan/zoom/drag; two sprints dedicated (S15–16); named contingency donates S17 week (27) | Ship viewer + JSON-import maps first (FR-MAP-07), editor polish in v1.1 — Cacti users literally edit config files today |
| **R-04** | TSDB cardinality/perf surprises as estate grows | 2 | 4 | 8 | Label discipline + bounded values (05 §6); ingester cardinality guard; monthly capacity review (22 §6); NFR triggers pre-planned | vmcluster activation is a deployment change (ADR-004); worst case: shorten raw retention |
| **R-05** | Queue semantics bugs (dupes, loss, poison) corrupt pipeline trust | 3 | 4 | 12 | Publisher confirms + idempotent consumers + DLQ everywhere (05 §4, 23 §3); pipeline integration tests + chaos-lite (24) | DLQ replay tooling; VM dedup makes metric double-writes harmless |
| **R-06** | SNMPv3 quirks across vendors (engine IDs, time sync, priv variants) | 3 | 3 | 9 | gosnmp is battle-tested; sim includes v3-authPriv target; credential test action isolates auth class (FR-CRED-03) | Per-device v2c fallback (flagged legacy); vendor-specific session tuning in connector `attrs` |
| **R-07** | **Vendor MIB coverage gaps — ZTE/Ubiquiti health especially** | 4 | 3 | **12** | Layered generic fallback means traffic/system always works (10 §4); real-iron validation early in S17; risk-flagged in connector matrix | Ship those vendors "traffic + generic health, best-effort" — documented per-model support matrix; deepen post-v1 |
| **R-08** | Remote-site network realities (NAT, flaky WAN, strict firewalls) break poller model | 2 | 4 | 8 | Outbound-only AMQPS on one port (ADR-006); 15-min disk buffer + drain (07 §6); chaos test severs the link (24) | Compose-based poller for sites without k8s; buffer size is a knob; worst case site-local VM remote-write (design note in 29) |
| R-09 | On-prem cluster/service ops burden (PG, RMQ, VM care) exceeds solo capacity | 3 | 3 | 9 | Operators (CNPG), single-binary VM, boring versions; runbooks + nightly restore drill (25) | Managed/lighter substitutions (k3s, SQLite-backed dev profile); defer HA ambitions |
| R-10 | Master-key loss = credential vault loss | 1 | 5 | 5 | Key custody runbook, password-manager escrow (20 §7); restore drill includes key | Credentials are re-enterable (devices unaffected); painful, not fatal — documented recovery: re-key + re-enter secrets |
| R-11 | Security incident (credential DB is a network-wide skeleton key) | 2 | 5 | 10 | Threat model (20 §1), envelope crypto, write-only API, scan gates, authz matrix tests, audit trail | Incident runbook: rotate KEK + all SNMP credentials via API, audit-log forensics, device ACLs limit blast |
| R-12 | Dependency/licensing shifts (React Flow, uPlot, VM licensing) | 2 | 3 | 6 | Wrappers isolate libs (14); VM is Apache-2.0 core; ADR required for infra swaps | Charts: uPlot→ECharts swap behind `<TimeSeries/>`; map canvas is the costliest swap — pinned versions + vendored if needed |
| R-13 | Load targets missed at 500-dev scale late (S19) | 2 | 4 | 8 | Nightly load from S10 (25) — regressions surface months early, not at S19 | NFR-19 skip-and-count degrades gracefully; interval relaxation as stopgap; capacity triggers early |
| R-14 | AI-generated code quality drift (subtle bugs at velocity) | 3 | 4 | 12 | The docs-as-spec discipline + high domain coverage bars + invariant tests (24) + arch-boundary lint — the CI is designed as the AI's reviewer | Slow down: human review of domain/crypto/authz paths (already flagged as ≥90% coverage zones) |
| R-15 | Pilot reveals product gaps (real NOC workflows ≠ design) | 3 | 3 | 9 | Personas from real users (02); headless usefulness from M2 means informal pilot feedback starts 4 months early | S20 fix window; v1.1 fast-follow committed in roadmap |

## Top-3 watch items (reviewed at every phase gate)

1. **R-02 scope creep** — the design package is deliberately opinionated about P0/P1/P2; every "while we're at it" goes through DECISIONS.md.
2. **R-01 solo continuity** — the AI-native repo is the mitigation; the measure of success: a fresh agent can pick up any sprint from docs alone (tested informally each phase).
3. **R-07 vendor MIBs** — the only risk owned by the outside world; early real-hardware access for Huawei/ZTE should be arranged **now** (procurement lead time), not at S17.
