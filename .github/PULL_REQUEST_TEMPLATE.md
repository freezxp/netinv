<!--
Thanks for the patch. Delete anything below that does not apply — this is a
checklist, not a form to be filled in exhaustively.
-->

## What this changes

<!-- And why. The *why* is the part that will still matter in a year. -->

## How you verified it

<!--
"Tests pass" is not verification on its own; the tests may not cover it. Say
what you actually observed — against a real device, against snmpsim, in the UI.
If a metric changed, say what it read before and after.
-->

## Checklist

- [ ] `make test lint` passes
- [ ] Commits follow conventional format (`feat:`, `fix:`, `docs:`, `chore:`)
      and explain *why* in the body
- [ ] **If this changes behaviour a doc describes, the doc is updated in the
      same commit** (NFR-70 — docs are the source of truth, see
      [CONTRIBUTING.md](../blob/main/CONTRIBUTING.md))
- [ ] Does not silently contradict [DECISIONS.md](../blob/main/DECISIONS.md);
      if it revisits a decision, the ADR is amended here too
- [ ] Uses the vocabulary from `docs/16-domain-model.md` (Device, Poller,
      Connector, Site — as defined there)

### If this touches a connector

- [ ] `make connector-lint` passes
- [ ] Zero diffs outside `connectors/` plus the one registry line (NFR-72)
- [ ] Capabilities declared honestly — a connector reporting a metric the
      device does not really expose is worse than one reporting nothing
- [ ] Recorded `snmpwalk` fixture added under `testdata/` if you have the
      hardware

### If this touches metrics or queries

- [ ] No bare `or` between two named metrics. MetricsQL matches on labels
      *excluding* `__name__`, so they collapse into one series and the second
      disappears with no error. This has caused three separate bugs — use
      `trafficExpr`/`seriesExpr` in `frontend/src/api/hooks.ts`
- [ ] `on(...)` replaced with `group_left()` anywhere `device` or `site` labels
      need to survive the join
