# Contributing to NetInv

Thanks for looking. NetInv is a network monitoring platform built by one
developer pairing with an AI, and it is at the stage where outside eyes are
worth more than outside code — particularly if you own hardware we have never
tested against.

## The most valuable thing you can do

**Run a connector against real hardware and tell us what happened.**

Four of the seven connectors — `cisco-ios`, `juniper-junos`, `huawei-vrp`,
`zte-zxr` — are written from MIB specifications and recorded fixtures. They
have never met a real device. The three that *have* (`generic`, `ubiquiti`,
`ruckus`) each needed corrections that no amount of spec-reading would have
found: a Ruckus R710 reports its port speed in `ifSpeed` and leaves
`ifHighSpeed` at zero; UniFi consoles report a net-snmp `sysObjectID` and so
never matched on vendor prefix at all.

Every one of those was a silent wrong number, not an error. That is the class
of bug we need help with. Open a
[hardware validation report](../../issues/new?template=hardware_validation.yml)
even if everything worked — a confirmed-good model is useful data.

## Ground rules

**The design docs are the source of truth.** `docs/` holds a 30-document
package, and code follows it. If your change makes the code diverge from a
doc, update the doc *in the same commit* (NFR-70). A PR that changes behaviour
described in a doc without touching the doc will be asked to.

**Architecture decisions live in [DECISIONS.md](DECISIONS.md).** Never silently
contradict one. If a decision needs revisiting, propose the change there first
— an ADR amendment is a perfectly good PR on its own.

**Terminology is fixed.** `docs/16-domain-model.md` defines the ubiquitous
language. A *Device* is a managed network element, a *Poller* is a site-local
collection agent, a *Connector* is a vendor driver. Use those words.

**Conventional commits, one logical unit per commit**: `feat:`, `fix:`,
`docs:`, `chore:`. Write the *why* in the body — the log is the project's
memory and it gets read.

## Getting a dev environment

```bash
make dev          # PostgreSQL, Redis, RabbitMQ, VictoriaMetrics, snmpsim, mailhog
make run-api      # and run-scheduler, run-poller, run-ingester, run-alerter, run-notifier
make frontend-dev
```

`api`, `scheduler` and `notifier` must share the same `NETINV_MASTER_KEY` —
it unlocks the credential vault, and they will refuse to decrypt each other's
secrets without it. The bundled SNMP simulator listens on port `1161` with
communities `public` (generic) and `cisco`. `scripts/seed-demo.sh` gives you a
fleet to look at.

For the whole platform in containers instead, see
[docs/32-quickstart.md](docs/32-quickstart.md).

```bash
make test lint licenses
```

Integration tests need `NETINV_TEST_PG_DSN`. They create a throwaway database
per test and drop it afterwards — point it at a scratch PostgreSQL, never at
a deployment you care about. (This is not hypothetical advice: they used to
write into a live inventory.)

## Adding a connector

The full checklist is [doc 10 §6](docs/10-connector-architecture.md). In short:

1. `connectors/<name>`, implementing `Connector` and embedding `generic.Base`.
2. Declare capabilities **honestly**. A connector that reports a metric the
   device does not actually expose is worse than one that reports nothing —
   see the `ruckus` entry in doc 10, which deliberately publishes no CPU or
   temperature because an R710 genuinely has none to give.
3. Record `snmpwalk` fixtures from real hardware into `testdata/*.snmpwalk`
   and write table-driven tests against them. **Redact the identity fields
   before committing** — serial number, hostname, management address. A fixture
   is a recording of someone's actual equipment, and it is going into a public
   repository. The mapping logic under test does not care what those strings
   say; this project committed a real serial exactly this way and only caught
   it while preparing to publish.
4. Register in `connectors/registry`, run `make connector-lint`.
5. **Zero diffs outside `connectors/`** plus the one registry line. CI enforces
   this with a path check; it is the property that makes the plugin framework
   worth having (NFR-72).

## Things that have bitten us

Worth knowing before you touch the relevant area:

- **MetricsQL `or` matches on labels excluding `__name__`.** Two metrics that
  differ only by name collapse into one, and the second vanishes with no error.
  This has caused three separate bugs (traffic in/out, memory used/total, AP
  up/total). Combine named metrics through `trafficExpr`/`seriesExpr` in
  `frontend/src/api/hooks.ts`, never a bare `or`.
- **PromQL `on(...)` drops every label you did not list**, including `device`
  and `site`. Use `group_left()` when you need them to survive.
- **Collection can stop while everything looks healthy.** The API answers, the
  UI loads, the scheduler keeps queueing — and nothing consumes. Twice. If you
  are debugging missing data, check queue consumer counts early.

## Security

Do not open a public issue for a vulnerability. See [SECURITY.md](SECURITY.md).

## Licence

By contributing you agree that your contributions are licensed under the
Apache License 2.0, matching the project ([LICENSE](LICENSE)). No CLA.
