# 19 — Kubernetes Deployment Design

**Status:** draft · **Depends on:** 18, ADR-006 · pipeline: 25

## 1. Cluster assumptions

Any CNCF-conformant 1.28+ on-prem cluster; reference: **RKE2** (prod) / **k3s** (lab). Required cluster services: an ingress controller (nginx), a default StorageClass (Longhorn/local-path/SAN CSI), cert management (cert-manager or corporate PKI), a LoadBalancer implementation for AMQPS (MetalLB or equivalent — else NodePort 5671).

## 2. Namespaces & workloads

| Namespace | Contents | Notes |
|---|---|---|
| `netinv` | 6 app Deployments + frontend, HPA (api/ingester), PDBs, NetworkPolicies | app chart |
| `netinv-data` | CloudNativePG cluster, VictoriaMetrics StatefulSet, Redis, RabbitMQ (operator or bitnami chart) | data chart deps |
| `netinv-poller` (remote clusters) | poller Deployment + buffer PVC | standalone chart |

Workload notes:
- All app pods: non-root, read-only rootfs, no privilege escalation, seccomp RuntimeDefault (poller additionally needs `NET_RAW` for ICMP — isolated in its own ServiceAccount/PSA exception, or use unprivileged UDP ping mode).
- Scheduler/alerter run 2 replicas with Redis lease leadership (doc 05 §9) — a Deployment, not StatefulSet.
- Probes: `/healthz` liveness, `/readyz` readiness (checks PG/Redis/AMQP connectivity with degraded-mode rules per NFR-25).
- Rolling updates: `maxUnavailable: 0` for api; consumers drain gracefully on SIGTERM (finish in-flight, requeue rest) within `terminationGracePeriodSeconds: 60`.

## 3. Helm charts

```
deploy/helm/netinv/            # umbrella: app + optional data-tier subcharts
  values.yaml                  # every option documented (NFR-54)
  values-prod.example.yaml
  templates/ (deployments, services, ingress, hpa, pdb, networkpolicy,
              servicemonitor, secrets-ref, migrations-job)
deploy/helm/netinv-poller/     # site_id, enroll token ref, core AMQPS endpoint, buffer size
```

Key values (excerpt): `image.tag` (pinned digest in prod), `api.replicas`, `ingress.host/tls`, `data.postgres.enabled` (bring-your-own toggle per store), `poller.siteId`, `retention.*` (doc 04 tiers), `masterKey.existingSecret`, `smtp.*`. Data-tier subcharts default **enabled** for the 30-minute quickstart (NFR-50), disable-able for externally managed DBs.

## 4. Configuration & secrets

- Config via ConfigMap → env; secrets via Kubernetes Secrets referenced as `existingSecret` (never templated values in prod): `netinv-master-key`, `netinv-jwt-key`, `netinv-pg`, `netinv-amqp`, per-site `netinv-poller-enroll`.
- Optional SealedSecrets/SOPS pattern documented for GitOps users; External Secrets Operator seam for the future Vault ADR (doc 20 §7).
- Migrations run as a Helm pre-upgrade Job (goose up, leader-locked) — chart upgrade = schema upgrade, one knob (NFR-51).

## 5. Network policies (default-deny posture)

- `netinv` namespace: deny all ingress except ingress-controller→api/frontend; deny egress except: api/alerter→VM+PG+Redis+AMQP, ingester→VM+AMQP, notifier→AMQP+PG+external 587/443, poller→AMQP+UDP161/ICMP to device CIDRs (values-provided list).
- `netinv-data`: only `netinv` pods on the specific ports; RabbitMQ additionally from the LB for remote pollers.

## 6. Autoscaling & priorities

- HPA: api (CPU 70%, 2–6), ingester (queue-depth via KEDA on `metrics.raw` — v1 optional, documented). Pollers scale manually per site (device count driven).
- PriorityClasses: data tier > collection path (scheduler/poller/ingester/rabbitmq) > api/frontend > notifier — under node pressure, collection survives first (it's the product).

## 7. Upgrade & rollback runbook (summary; full runbook ships with chart README)

1. `helm upgrade` staging → smoke suite (doc 25) → prod during window.
2. Chart keeps 2 revisions; `helm rollback` is safe one version back (expand-migrate-contract guarantees schema compat, NFR-51).
3. Data tier upgraded independently (CNPG minor bumps, VM binary bump with snapshot first).
4. Poller charts upgrade last; core tolerates one-version-old pollers (doc 10 §3 version skew).

## 8. The 100k-device shape (activation only, no redesign — ADR-004)

vmcluster subchart swap (vminsert/vmselect/vmstorage ×N) · CNPG adds replicas + PgBouncer · RabbitMQ 3-node quorum · KEDA-driven ingester fleet · per-site poller Deployments scale replicas · api split into read/write Deployments (same image, role flag) per NFR trigger table.
