# netinv — core-site Helm chart

Installs NetInv's seven services, the frontend, and (by default) a single-replica
data tier, per [doc 19](../../../docs/19-kubernetes-design.md).

**Status: installed and verified on Kubernetes 1.35 (minikube), 2026-08-14.**
All 17 pods reach ready in about 100 seconds from `helm install`; migrations
run, login works end to end through the Ingress, and a `helm upgrade` preserves
the database without rotating its password. Every manifest also passes
`helm lint` and `kubeconform --strict` across six value sets in CI.

What that install is *not*: a soak. It has not run for 72 hours, nothing has
been killed underneath it, and it has never held a real fleet. The chart version
stays below 1.0.0 until it has. Expect to find problems on cluster shapes other
than this one; please report them.

## Install

The master key is yours to create and keep. It encrypts every stored device
credential, it is deliberately absent from backups ([doc 20 §12.3](../../../docs/20-security-design.md)),
and a key that exists only inside a Helm release is one `helm uninstall` from
taking the credential vault with it.

```bash
kubectl create namespace netinv
kubectl -n netinv create secret generic netinv-keys \
  --from-literal=NETINV_MASTER_KEY="$(openssl rand -base64 32)" \
  --from-literal=NETINV_JWT_SIGNING_KEY="$(openssl rand -base64 32)" \
  --from-literal=NETINV_ADMIN_PASSWORD="$(openssl rand -base64 18)"

helm upgrade --install netinv deploy/helm/netinv -n netinv \
  --set ingress.host=netinv.your.domain
```

Then put that master key somewhere durable. Restoring a backup without it gives
you the inventory and the history but no usable credentials.

For production, start from `values-prod.example.yaml`: external data stores, a
pinned image digest, a real certificate, and the flow Service exposed to the
device network.

## What it deploys

| Component | Shape | Notes |
|---|---|---|
| api | 2 replicas, `maxUnavailable: 0` | Runs schema migrations at startup under a Postgres advisory lock, so replicas serialise |
| scheduler, alerter | 2 replicas, leader-elected via Redis | Deployment, not StatefulSet (doc 05 §9) |
| ingester | 2 replicas | Queue-depth autoscaling needs KEDA and is not wired |
| poller | 1 replica | Core-site poller; remote sites use the `netinv-poller` chart |
| notifier | 1 replica | Lowest priority class — a queued notification is late, not lost |
| flow | **exactly 1 replica** | UDP with no replay, and v9/IPFIX template state is per exporter. A second replica would see half a stream and none of its templates |
| frontend | 2 replicas | Serves the SPA only; the ingress routes `/api` |
| data tier | 1 replica each | Postgres, VictoriaMetrics, RabbitMQ as StatefulSets with PVCs; Redis unpersisted (leases and cache only) |

## Things that will bite

Each of these was found by installing the chart, not by reading it.

- **A no-TLS Ingress breaks login, and looks like a broken app.** Session cookies
  are `Secure` by default, so over plain HTTP the browser accepts the login
  response and never sends the cookie back — login appears to succeed, and the
  next request is a 401. Set `ingress.tlsSecret`. The chart prints this warning
  on install when the combination is present.
- **A second release in the same cluster fails to install.** PriorityClasses are
  cluster-scoped with fixed names, so release two hits `invalid ownership
  metadata`. Set `priorityClasses.create=false` on every release after the
  first; the pods still resolve the names created by the first.
- **The api crash-loops on a first install** until Postgres accepts connections
  — it exits rather than waits. Three or four restarts before it settles is
  normal, not a fault.

- **`retention` is one setting read by two components.** VictoriaMetrics stores
  that long and the API refuses range queries beyond it — it rejects rather than
  clamps. Set it below the store's real retention and the UI hides history you
  are paying to keep; above, and "Last Year" errors instead of charting.
- **The flow collector must be reachable from the device network.** It defaults
  to `ClusterIP`, which no exporter can reach. Set `services.flow.serviceType` to
  `LoadBalancer` (needs MetalLB or equivalent on-prem) or `NodePort`. Export is
  also selected per network on most gateways, and an unticked network is
  silently absent — see [doc 34 §4.2](../../../docs/34-flow-collection.md).
- **NetworkPolicy is off by default on purpose.** On a cluster whose CNI does not
  enforce it, these objects are accepted by the API server and do nothing, which
  is indistinguishable from a working policy. Turn it on only once you know your
  CNI enforces it, and set `networkPolicy.deviceCIDRs` — the chart refuses to
  render a policy that would deny the poller every device.
- **ICMP under NetworkPolicy.** NetworkPolicy cannot express a protocol without a
  port, so availability polling rides on the portless CIDR rule. Calico and
  Cilium admit it; others drop ICMP while SNMP keeps working, which presents as
  every device down over ping with its graphs still filling.
- **Generated store passwords are read back from the Secret on upgrade.** With no
  cluster to read — `helm template`, `--dry-run`, offline GitOps rendering — a
  fresh password is generated each render. Set `data.postgres.password` and
  `data.rabbitmq.password` explicitly if you render manifests outside the
  cluster, or an upgrade will hand the app a new password for a database whose
  password never changed.

## Upgrade and rollback

1. `helm upgrade` on staging, run the smoke suite (doc 25), then production in a
   window.
2. Schema migrations run inside the api container at startup, guarded by an
   advisory lock, so a chart upgrade is a schema upgrade — one knob (NFR-51).
3. `helm rollback` is safe one version back: migrations are
   expand-migrate-contract, so the previous binary still runs against the new
   schema.
4. The data tier upgrades independently of the app. Snapshot VictoriaMetrics
   before bumping it, and rehearse the restore rather than assuming it
   (doc 20 §12.3 is what happens when you do not).
5. Poller charts upgrade last; the core tolerates one-version-old pollers
   (doc 10 §3).
