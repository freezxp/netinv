# 31 — Pilot & Production Runbook

**Status:** draft · **Depends on:** 18, 19, 20 · Owner action required (Sprint 20)

This is the operator's checklist to take NetInv from a fresh install to a
running pilot across the core site + remote datacenters. The mechanics of the
Helm install itself live in [doc 35](35-kubernetes-deployment.md); this is what
to do once it is up. It is deliberately concrete;
everything not requiring your real infrastructure has already been executed and
verified (see the sprint commit log and doc 24 §4 drills).

## 0. Prerequisites

- On-prem Kubernetes 1.28+ (RKE2 reference), a default StorageClass, an ingress
  controller, and a LoadBalancer or NodePort path for AMQPS 5671.
- A data tier. The chart installs a single-replica one by default, which is a
  lab shape; production disables it per store and points at managed PostgreSQL
  16, VictoriaMetrics, Redis and RabbitMQ (doc 35 §3).
- `helm`, `kubectl`, and pull access to `ghcr.io/freezxp/netinv-*` — note the
  packages are **private**, so a cluster without a pull secret must build the
  images from source.

## 1. Secrets (never commit these)

```bash
# 32-byte credential-vault master key (ADR-011) and JWT signing seed.
kubectl create secret generic netinv-keys -n netinv \
  --from-literal=NETINV_MASTER_KEY="$(openssl rand -base64 32)" \
  --from-literal=NETINV_JWT_SIGNING_KEY="$(openssl rand -base64 32)" \
  --from-literal=NETINV_ADMIN_PASSWORD="$(openssl rand -base64 18)"
```
Record the master key in the team password manager. **Losing it means
re-entering every SNMP credential** (devices are unaffected — doc 28 R-10).

## 2. Install the core site

```bash
helm upgrade --install netinv deploy/helm/netinv -n netinv --create-namespace \
  --set ingress.host="netinv.your.domain" \
  --set ingress.tlsSecret="netinv-tls"
```

That installs the chart's own data tier. To use managed stores, disable them and
set `connections.*` instead — see doc 35 §3 and `values-prod.example.yaml`.

The api runs migrations on boot. It also **restarts three or four times on a
first install**, because it exits rather than waits while Postgres is still
starting; that is expected, not a fault. The initial admin password is the
`NETINV_ADMIN_PASSWORD` you put in the Secret in step 1:

```bash
kubectl -n netinv get secret netinv-keys \
  -o jsonpath='{.data.NETINV_ADMIN_PASSWORD}' | base64 -d
```

Set `services.poller.env.NETINV_SITE_ID` to the core site's id after creating
the site (step 4).

## 3. First login & hardening (doc 20 §12 checklist)

- [ ] Log in as `admin`, complete the forced password change.
- [ ] Create your own admin user; keep `admin` as break-glass.
- [ ] Confirm TLS on the ingress (HSTS), and that `/api/v1/auth/refresh` sets a
      Secure cookie (i.e. `NETINV_INSECURE_COOKIES` is unset).
- [ ] Configure the notification channels (Settings) and send a test to each.
- [ ] Verify `/metrics` is only reachable in-cluster, not through the ingress.

## 4. Onboard a site and its poller

1. **Create the site**: Platform → Sites → Add (region/DC as needed).
2. **Issue an enrollment token**: Platform → Pollers → Enroll (name + site) →
   copy the one-time token (15-min TTL).
3. **Install the remote poller** at that datacenter:
   ```bash
   kubectl create secret generic netinv-poller-enroll -n netinv-poller \
     --from-literal=NETINV_ENROLL_TOKEN="<token>"
   helm install poller deploy/helm/netinv-poller -n netinv-poller --create-namespace \
     --set core.amqpURL="amqps://SITEUSER:PASS@core.your.domain:5671/" \
     --set core.apiURL="https://netinv.your.domain"
   ```
   The poller registers itself and appears as **pending**. Only outbound 5671
   and 443 are needed from the site (ADR-006) — no inbound rules.
4. **Approve** it in Platform → Pollers.

## 5. Onboard devices (start with 2–3 per vendor)

1. Platform → Credentials → add the SNMP credential (prefer v3 authPriv).
   Use "Test" against a known device before saving.
2. Inventory → Add device (or CSV import): name, mgmt IP, site, credential,
   connector (leave blank to auto-match on first sync), snmp_port if not 161.
3. Within ~2 minutes the device syncs (identity + interfaces + LLDP) and
   graphs begin. Watch Platform → Pollers for heartbeat + poll counts.
4. If it is still `pending` after that, run `scripts/diagnose-pending.sh` — a
   device stuck in `pending` is silent by default and §9 explains why.

## 6. Validate before widening (this is the pilot's real work)

- [ ] **Per-vendor connector check**: for each of Cisco / Juniper / Huawei /
      ZTE / Ubiquiti, confirm the device detail Health tab shows real CPU /
      memory / temperature (not empty). Record verified model + OS in each
      connector's README. ZTE and Huawei are the risk items (R-07) — if health
      is empty, the device still delivers traffic/availability; open an issue
      with an `snmpwalk` capture (`scripts/record-fixture.sh`, which redacts identity at capture time, including the
      first connector fix) so the OID map can be extended.
- [ ] **Alert path**: shut an interface (or `admin-down`) and confirm the alert
      fires and notifies within 60 s; ack it; bring it back and confirm resolve.
- [ ] **Weathermap**: build a small map of the core links, bind interfaces,
      publish, and confirm live utilization coloring on the wall view.
- [ ] **Availability**: confirm ICMP RTT/loss graphs and the dashboard
      availability % look sane for a known-good and a known-flaky link.

## 6a. Mirroring metrics to a second VictoriaMetrics

`NETINV_VM_MIRROR_URL` (comma-separated) makes the ingester and `netinv-flow` copy every import batch to one or more backup instances:

```bash
# deploy/compose-app/.env
NETINV_VM_MIRROR_URL=http://vm-backup.example.internal:8428
```

Recreate `ingester` and `flow` afterwards. Confirm it is working from the counters on either service's `/metrics`, not from the absence of errors:

```bash
docker exec netinv-ingester-1 wget -qO- localhost:8080/metrics | grep netinv_vm_mirror
```

`netinv_vm_mirror_samples_total` rising and `netinv_vm_mirror_failed_total` flat is a healthy mirror. A rising failure counter is a hole in the copy that **nothing backfills** — the mirror is best-effort by design, because a backup target that can stall production ingest is worse than no backup at all. Judge it by the counters; the log is rate-limited to one line a minute per target so an hour-long outage cannot bury the rest of the service's output.

What this is not: a guaranteed-complete copy. It holds what arrived while the mirror was reachable. If you need every sample, put **vmagent** in front — it has a persistent queue and replays what it could not deliver. Mirroring here is for a warm standby or a longer-retention archive, and it composes with `scripts/backup.sh`, which snapshots the primary rather than streaming it.

## 7. Backup schedule

Wire `scripts/backup.sh` (or its k8s equivalent) into a nightly CronJob to
off-cluster storage. RPO target: 24 h, tightening to WAL archiving per the
roadmap.

**Rehearse the restore, and rehearse it by checking the data, not the exit
code.** This section previously said the drill "has been executed successfully
against dev data". That was wrong. When it was finally run against pilot data on
2026-08-13 the metrics half of the restore turned out to restore nothing at all
while exiting 0 and leaving VictoriaMetrics reporting healthy — see doc 20 §12.3
for the three defects and the fixes. A restore that has only been observed to
exit 0 is an untested restore.

`scripts/restore.sh` refuses the production containers unless given `--force`,
and prints the scratch-container recipe when it does. Run it monthly:

```
PG=drill-pg VM=drill-vm VM_PORT=18428 ./scripts/restore.sh <backup-dir>
```

Two things to know before you read the output as a failure or a success:

- **Compare at a timestamp inside the backup window.** The restored store's
  newest sample is the moment the snapshot was taken, so an instant query at
  *now* legitimately returns fewer series than live.
- **`NETINV_MASTER_KEY` is not in the backup**, deliberately. Without it a
  restore gives you the inventory and the history but no usable credentials, so
  the key belongs in your password manager, not only in the deployment.

## 8. Widen the rollout

Once the per-vendor checks pass and the alert/backup loops are proven, grow the
fleet site by site. Watch the capacity triggers (doc 04 §1): at ~5k devices
move VictoriaMetrics to `vmcluster` and add PgBouncer; the app itself does not
change (ADR-004).

## 9. When it looks healthy and the data is wrong

Every fault below was found on the pilot, and each one presented as a *healthy*
system: devices up, polls succeeding, no errors logged, graphs drawing. The
green tick is not the check. Work from the data.

**First: is collection actually advancing?** Judge it by the counter, never by
query freshness — `timestamp(last_over_time(m[10m]))` in an instant query does
not advance reliably under VictoriaMetrics' `-search.latencyOffset` and result
caching, and reads as a dead stall on a perfectly healthy stack.

```bash
curl -s localhost:8428/metrics | grep '^vm_rows_inserted_total'   # twice, 60s apart
```

A fleet of 232 interfaces on a 60 s cadence is roughly 32 rows/s.

**A device graphs some interfaces and not others.** The agent's walk is broken
while its data is fine — compare a walk against a GET for the same OID:

```bash
snmpwalk -v2c -c <community> <host> 1.3.6.1.2.1.2.2.1.10 | wc -l
snmpget  -v2c -c <community> <host> 1.3.6.1.2.1.2.2.1.10.<if_index>
```

Far fewer walk rows than `ifOperStatus` is this fault. The connector repairs it
with targeted GETs and reports `netinv_if_counters_repaired` — zero on a healthy
agent (doc 10 §7). Restart `snmpd` on the device; the repair keeps the graphs
honest until you do, and the metric returning to 0 is how you confirm the fix
took rather than the connector still compensating.

**A poller must be told which sites to serve, unless it is told to serve them all.** `NETINV_SITE_ID` takes a comma-separated list or `*`. With a list, a site created afterwards is not consumed: its jobs are published to a queue nobody reads, no error is raised anywhere, and its devices are simply never polled. With `*` the poller consumes whatever the scheduler announces on the `sites.active` fanout — sites appear and disappear without an environment edit or a restart. A poller deliberately restricted to part of the estate keeps an explicit list; everything else should use `*`.

**A device is added and never leaves `pending`.** A device leaves `pending` in
exactly one place: the sync apply transaction setting `status='active'` when a
good snapshot lands. Nothing else promotes it, so ICMP, traffic and health can
all succeed — graphs and all — while the device sits in `pending` forever. Four
distinct faults present identically, and `scripts/diagnose-pending.sh` tells
them apart:

```bash
./scripts/diagnose-pending.sh                 # every pending device
./scripts/diagnose-pending.sh juniper-junos   # one connector
```

| Class | Cause | Where it shows |
|---|---|---|
| A | The device's profile omits `sync` from `families_enabled`, so the schedule row was dropped and nothing is dispatched | no `sync=` row in `platform.polling_schedule` |
| B | No poller consumes the device's site queue, so jobs accumulate unread | the device page banner names the site; also `poll.site.<site>` with `consumers = 0` |
| C | The poller ran it and SNMP failed | `platform.sync_runs.error` |
| D | Collection succeeded but the apply is erroring and requeuing | `sync result requeued` in the api log |

The device detail page carries the same diagnosis: a banner above the tabs
names whichever of A/B, C or D applies, and the History tab lists every sync
run with the failure reason in full (doc 30 §5, `GET /devices/{id}/sync-runs`).
Until 2026-08-18 it did not — `platform.sync_runs` was written on every poll
and read by nothing, so a device could fail for a stated, recorded reason that
an operator had no way to see short of psql. Class B is named there too since 2026-08-18: the
scheduler already declares every site queue before publishing to it, and
`queue.declare` reports the consumer count, so it now records that per site
(migration 0013) and warns once on the transition — `site has no poller
consuming its queue`. The script stays because it is the tool for a deployment
whose UI you cannot reach, and because it reads the broker directly rather than
what the scheduler last observed.

When the error is a timeout and a manual `snmpwalk` succeeds, reproduce from
the poller's own network position rather than from a shell — an ACL or a Junos
`routing-instance-access` restriction keys on the source address, and the
poller image is distroless, so attach a sidecar to its network namespace:

```bash
docker run --rm --network container:netinv-poller-1 alpine sh -c \
  'apk add -q net-snmp-tools && time snmpbulkwalk -v2c -c <community> \
   -Cr25 <host> 1.3.6.1.2.1.2.2 | wc -l'
```

Time it: inventory walks `ifTable` *and* `ifXTable` inside a 60 s job budget, so
a walk that is merely slow by hand is a hard failure in the poller. Raise
`snmp_timeout_ms` / `snmp_retries` on the profile, or cut the interface count at
the device.

**A whole site is losing polls while reporting success.** A leaked AMQP consumer
takes a share of the jobs and acks none:

```bash
rabbitmqctl list_queues name messages messages_unacknowledged consumers
```

More than one consumer on a site queue, or unacked messages with nothing ready,
is this fault (doc 07 §6.1).

**A device's inventory is frozen at an old topology.** A sync that cannot apply
is requeued every second forever:

```bash
docker logs netinv-api-1 | grep "sync result requeued"
```

A repeating `duplicate key ... interfaces_device_id_if_index` is the reindex
case (doc 11 §3.1). Symptoms are indirect: interface names and states stop
matching the device, and any weathermap link on a renumbered interface goes
flat.

**A weathermap link reads `nodata` while the interface is busy.** Check the
device's Interfaces tab first: if the interface is passing traffic, the link is
resolving to an ifIndex the device no longer has. Confirm inventory is current
(the check above), then re-open the map — links resolve their index at render
time (doc 30 §3), so a healthy sync fixes the link with no edit.

## Rollback

`helm rollback netinv` is safe one version back — migrations are
expand-migrate-contract (NFR-51). The poller charts upgrade last and the core
tolerates ±1 poller version (doc 10 §3).

Use `upgrade.sh --latest` from a script or a cron entry rather than the bare
form: a plain run degrades to deploying the working tree when it cannot pull —
right for a person at a terminal, wrong for automation, where deploying older
code while reporting success is the failure that matters.

On Compose, `deploy/compose-app/upgrade.sh` is the equivalent path in both
directions: it backs up before it touches anything, and prints the exact
rollback — check out the previous commit and re-run it — when it finishes. That
restores the previous binaries only; undoing a migration means restoring the
data from the backup it took (doc 32, Upgrading).
