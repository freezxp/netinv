# 31 — Pilot & Production Runbook

**Status:** draft · **Depends on:** 18, 19, 20 · Owner action required (Sprint 20)

This is the operator's checklist to take NetInv from `v1.0.0-rc.1` to a running
pilot across the core site + remote datacenters. It is deliberately concrete;
everything not requiring your real infrastructure has already been executed and
verified (see the sprint commit log and doc 24 §4 drills).

## 0. Prerequisites

- On-prem Kubernetes 1.28+ (RKE2 reference), a default StorageClass, an ingress
  controller, and a LoadBalancer or NodePort path for AMQPS 5671.
- Data tier reachable from the cluster: PostgreSQL 16, VictoriaMetrics, Redis,
  RabbitMQ (bring-your-own for rc.1; the CNPG/VM/RabbitMQ subcharts are the
  doc-19 §3 follow-up).
- `helm`, `kubectl`, and pull access to `ghcr.io/freezxp/netinv-*`.

## 1. Secrets (never commit these)

```bash
# 32-byte credential-vault master key (ADR-011) and JWT signing seed.
kubectl create secret generic netinv-keys -n netinv \
  --from-literal=NETINV_MASTER_KEY="$(openssl rand -base64 32)" \
  --from-literal=NETINV_JWT_SIGNING_KEY="$(openssl rand -base64 32)"
```
Record the master key in the team password manager. **Losing it means
re-entering every SNMP credential** (devices are unaffected — doc 28 R-10).

## 2. Install the core site

```bash
helm install netinv deploy/helm/netinv -n netinv --create-namespace \
  --set data.pgDSN="postgres://netinv:PASS@postgres:5432/netinv" \
  --set data.amqpURL="amqp://netinv:PASS@rabbitmq:5672/" \
  --set data.vmURL="http://victoriametrics:8428" \
  --set ingress.host="netinv.your.domain" \
  --set ingress.tlsSecret="netinv-tls" \
  --set uiURL="https://netinv.your.domain"
```
The api runs migrations on boot and prints the **bootstrap admin password
once** — grab it from `kubectl logs deploy/netinv-api -n netinv | grep bootstrap`
and change it at first login. Set `services.poller.env.NETINV_SITE_ID` to the
core site's id after creating the site (step 4).

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
off-cluster storage, and run `scripts/restore.sh` against a scratch namespace
monthly (the drill in doc 24 §4 is the template — it has been executed
successfully against dev data). RPO target: 24 h for rc.1, tightening to WAL
archiving per the roadmap.

## 8. Widen the rollout

Once the per-vendor checks pass and the alert/backup loops are proven, grow the
fleet site by site. Watch the capacity triggers (doc 04 §1): at ~5k devices
move VictoriaMetrics to `vmcluster` and add PgBouncer; the app itself does not
change (ADR-004).

## Rollback

`helm rollback netinv` is safe one version back — migrations are
expand-migrate-contract (NFR-51). The poller charts upgrade last and the core
tolerates ±1 poller version (doc 10 §3).
