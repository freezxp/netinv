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

## Rollback

`helm rollback netinv` is safe one version back — migrations are
expand-migrate-contract (NFR-51). The poller charts upgrade last and the core
tolerates ±1 poller version (doc 10 §3).
