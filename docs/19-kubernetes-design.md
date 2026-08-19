# 19 — Kubernetes Deployment Design

**Status:** draft · **Depends on:** 18, ADR-006 · pipeline: 25

> This is the *design*: what the workloads are and why. For the *procedure* —
> installing the chart, the data-tier toggles, and the failures the first real
> install produced — see [doc 35](35-kubernetes-deployment.md).

## 1. Cluster assumptions

Any CNCF-conformant 1.28+ on-prem cluster; reference: **RKE2** (prod) / **k3s** (lab). Required cluster services: an ingress controller (nginx), a default StorageClass (Longhorn/local-path/SAN CSI), cert management (cert-manager or corporate PKI), a LoadBalancer implementation for AMQPS (MetalLB or equivalent — else NodePort 5671).

## 2. Namespaces & workloads

| Namespace | Contents | Notes |
|---|---|---|
| `netinv` | 7 app Deployments + frontend, HPA (api), PDBs, PriorityClasses, NetworkPolicies, and by default the data tier | app chart |
| `netinv-data` | Externally managed stores when `data.<store>.enabled=false`: CloudNativePG, VictoriaMetrics, Redis, RabbitMQ operator. Not created by the chart | operator-managed |
| `netinv-poller` (remote clusters) | poller Deployment + buffer PVC | standalone chart |

Workload notes:
- All app pods: non-root (uid 65532), read-only rootfs with an emptyDir at `/tmp`, all capabilities dropped, no privilege escalation, seccomp RuntimeDefault. The core-site poller uses unprivileged UDP ping rather than `NET_RAW`; the frontend additionally needs writable `/var/cache/nginx` and `/var/run`, or nginx fails to start rather than degrading.
- Scheduler/alerter run 2 replicas with Redis lease leadership (doc 05 §9) — a Deployment, not StatefulSet.
- **Flow runs exactly one replica and needs a Service reachable from the device network**, unlike every other component: exporters push to it. The chart renders a Service for any component declaring `listen` ports, defaulting to `ClusterIP`; a real deployment sets `services.flow.serviceType` to `LoadBalancer` or `NodePort`, because how devices reach the cluster is a property of the network rather than of the chart. UDP 2055 and 4739 are both bound (doc 34 §2), and the NetworkPolicy has to admit them from the device subnets.
- Probes: `/healthz` liveness, `/readyz` readiness (checks PG/Redis/AMQP connectivity with degraded-mode rules per NFR-25).
- Rolling updates: `maxUnavailable: 0` for api; consumers drain gracefully on SIGTERM (finish in-flight, requeue rest) within `terminationGracePeriodSeconds: 60`.

## 3. Helm charts

```
deploy/helm/netinv/
  Chart.yaml                   # version == appVersion == the product's release version
  values.yaml                  # every option documented (NFR-54)
  values-prod.example.yaml     # external data tier, pinned digest, real cert
  README.md                    # install + upgrade/rollback runbook (§7)
  templates/ (_helpers, deployments, datatier, service-ingress,
              priorityclasses, pdb-hpa, networkpolicy, servicemonitor)
deploy/helm/netinv-poller/     # site_id, enroll token ref, core AMQPS endpoint, buffer size
```

Key values (excerpt): `image.tag` (pinned digest in prod), `services.<name>.replicas`, `ingress.host/tlsSecret`, `data.<store>.enabled` (bring-your-own toggle per store), `services.poller.env.NETINV_SITE_ID`, `retention`, `secrets.existingSecret`. The data tier defaults **enabled** for the 30-minute quickstart (NFR-50), disable-able per store for externally managed ones.

Two things here differ from the original plan, and the plan was wrong rather than the code:

- **The data tier is rendered by this chart, not pulled in as subcharts.** Subcharts meant CloudNativePG and a RabbitMQ operator, both of which have to be installed cluster-wide *before* the quickstart can run — which is not a quickstart. `templates/datatier.yaml` renders one replica of each store with a PVC, explicitly a lab shape; production disables them and points `connections.*` at managed stores. That keeps NFR-50 achievable without pretending a single-replica StatefulSet is an HA database.
- **There is no migrations Job.** `backend/cmd/api` already runs goose at startup under a Postgres advisory lock (`internal/platform/pgx/migrate.go`), so replicas serialise and a chart upgrade is a schema upgrade with no extra moving part. A pre-upgrade Job would be a second path to the same migrations, and two migration paths is how you get a schema applied twice in different orders.

There is also no `smtp.*`: notification channels are configured in the application and stored in Postgres, not passed as deployment config.

## 4. Configuration & secrets

- Config via ConfigMap → env; secrets via Kubernetes Secrets referenced as `existingSecret` (never templated values in prod): `netinv-master-key`, `netinv-jwt-key`, `netinv-pg`, `netinv-amqp`, per-site `netinv-poller-enroll`.
- Optional SealedSecrets/SOPS pattern documented for GitOps users; External Secrets Operator seam for the future Vault ADR (doc 20 §7).
- Migrations run inside the api container at startup (goose, serialised by a Postgres advisory lock) — chart upgrade = schema upgrade, one knob (NFR-51). This was planned as a Helm pre-upgrade Job; the api already did it, and a second migration path is how a schema gets applied twice in different orders.

## 5. Network policies (default-deny posture)

- `netinv` namespace: deny all ingress except ingress-controller→api/frontend; deny egress except: intra-namespace, DNS, notifier→external 25/465/587/443, poller→UDP161/ICMP to device CIDRs (values-provided list), and flow←device CIDRs on UDP 2055/4739.
- **Off by default (`networkPolicy.enabled=false`), deliberately.** On a cluster whose CNI does not enforce NetworkPolicy the API server accepts these objects and nothing enforces them, which is indistinguishable from a working policy — a security control the operator believes they have. Enabling it with an empty `deviceCIDRs` would deny the poller every device, so the chart refuses to render that combination rather than silently stopping collection.
- **ICMP cannot be expressed.** NetworkPolicy has no way to allow a protocol without a port, so availability polling rides on the portless CIDR rule. Calico and Cilium treat that as all-protocols; on CNIs that do not, ICMP is dropped while SNMP keeps working — every device reports down over ping while its graphs keep filling. That exact symptom has already cost this project time from an unrelated cause (doc 33 §4.2).
- `netinv-data`: only `netinv` pods on the specific ports; RabbitMQ additionally from the LB for remote pollers.

## 6. Autoscaling & priorities

- HPA: api (CPU 70%, 2–6), ingester (queue-depth via KEDA on `metrics.raw` — v1 optional, documented). Pollers scale manually per site (device count driven).
- PriorityClasses: `netinv-data` (1000) > `netinv-collection` (900: scheduler, poller, ingester, flow) > `netinv-app` (500: api, alerter, frontend) > `netinv-notify` (300) — under node pressure, collection survives first, because a gap in collection is a permanent hole in history while a gap in the UI is an outage you recover from. Alerter sits with the app rather than with collection: a missed evaluation re-runs against stored data. Cluster-scoped objects with global names, so `priorityClasses.create=false` for a cluster that already defines them.

## 7. Upgrade & rollback runbook (summary; full runbook ships with chart README)

1. `helm upgrade` staging → smoke suite (doc 25) → prod during window.
2. Chart keeps 2 revisions; `helm rollback` is safe one version back (expand-migrate-contract guarantees schema compat, NFR-51).
3. Data tier upgraded independently (CNPG minor bumps, VM binary bump with snapshot first).
4. Poller charts upgrade last; core tolerates one-version-old pollers (doc 10 §3 version skew).

## 8. The 100k-device shape (activation only, no redesign — ADR-004)

vmcluster subchart swap (vminsert/vmselect/vmstorage ×N) · CNPG adds replicas + PgBouncer · RabbitMQ 3-node quorum · KEDA-driven ingester fleet · per-site poller Deployments scale replicas · api split into read/write Deployments (same image, role flag) per NFR trigger table.
