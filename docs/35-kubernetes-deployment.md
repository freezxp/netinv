# 35 — Deploying NetInv on Kubernetes

**Status:** draft · **Depends on:** [19](19-kubernetes-design.md), [20](20-security-design.md), [31](31-pilot-runbook.md) · The practical guide to getting
NetInv running on a cluster with the Helm chart. [Doc 19](19-kubernetes-design.md) is the *design* — what
the workloads are and why; this is the *procedure*, plus everything that went
wrong the first time it was run for real.

> **Verified end to end on Kubernetes 1.35** (minikube, docker driver, 6 CPU /
> 10 GB) on 2026-08-14: 17/17 pods ready about 100 seconds after
> `helm install`, migrations applied, login working through the Ingress, and a
> `helm upgrade` preserving the database without rotating its password.
>
> **What that is not: a soak.** It has not run for 72 hours, nothing has been
> killed underneath it, and it has never held a real fleet. That gate is still
> open (ADR-023) and the chart version stays below 1.0.0 until it closes. On a
> cluster unlike the one above, expect to find things — and please report them.

## 1. What you need

- A CNCF-conformant cluster, 1.28 or newer. Reference shapes: **RKE2** or **k3s**
  for production, **minikube** or **kind** for a lab.
- An **ingress controller**. The chart assumes `nginx`; set `ingress.className`
  otherwise.
- A **default StorageClass**. Postgres, VictoriaMetrics and RabbitMQ each take a
  PVC.
- For flow collection, a way to expose UDP from outside the cluster —
  **LoadBalancer** (MetalLB or equivalent on-prem) or NodePort.
- **cert-manager**, or a certificate you can put in a Secret. Not optional in
  any meaningful sense: see §6.

Resource floor, measured on the verified install with the chart's own data tier
and no devices attached:

| | |
|---|---|
| All 17 pods | ~420 MiB |
| RabbitMQ | 130 MiB, 249m CPU — the largest single consumer |
| Postgres | 95 MiB |
| Each api replica | 69 MiB |
| The seven Go services together | under 160 MiB |

A 2-core / 4 GB node runs this. Fleet size drives it up from there; [doc 04](04-nfr.md) has
the capacity triggers.

## 2. Install

The master key is yours to create and to keep. It encrypts every stored device
credential, it is deliberately **not** in any backup ([doc 20 §12.3](20-security-design.md)), and a key
that exists only inside a Helm release is one `helm uninstall` away from taking
the credential vault with it.

```bash
kubectl create namespace netinv

kubectl -n netinv create secret generic netinv-keys \
  --from-literal=NETINV_MASTER_KEY="$(openssl rand -base64 32)" \
  --from-literal=NETINV_JWT_SIGNING_KEY="$(openssl rand -base64 32)" \
  --from-literal=NETINV_ADMIN_PASSWORD="$(openssl rand -base64 18)"

helm upgrade --install netinv deploy/helm/netinv -n netinv \
  --set ingress.host=netinv.your.domain \
  --set ingress.tlsSecret=netinv-tls
```

Put that master key somewhere durable **now**, before you forget it exists.
Restoring a backup without it gives you the inventory and the history and no
usable credentials.

Retrieve the initial admin password with:

```bash
kubectl -n netinv get secret netinv-keys \
  -o jsonpath='{.data.NETINV_ADMIN_PASSWORD}' | base64 -d
```

### Watching it come up

```bash
kubectl -n netinv get pods -w
```

**The api will restart three or four times on a first install. That is normal.**
It exits rather than waits when Postgres is not yet accepting connections, so
Kubernetes backs it off and retries; it settles once the data tier is ready.
Roughly 100 seconds end to end on the reference install.

## 3. The data tier

By default the chart installs one replica each of Postgres, VictoriaMetrics,
Redis and RabbitMQ, with PVCs for the three that hold state. Redis is not
persisted on purpose — it holds leader leases and caches, never the only copy of
anything.

**That is a lab shape.** It is not HA, it has no failover, and its backup story
is whatever you build around it. It exists so `helm install` produces a working
NetInv without first installing CloudNativePG and a RabbitMQ operator, because
operators as a prerequisite is not a quickstart (NFR-50).

For production, disable each store and point at something managed:

```yaml
data:
  postgres:        { enabled: false }
  victoriametrics: { enabled: false }
  redis:           { enabled: false }
  rabbitmq:        { enabled: false }

connections:
  pgDSN:     "postgres://netinv@pg-rw.netinv-data:5432/netinv?sslmode=require"
  redisAddr: "redis.netinv-data:6379"
  amqpURL:   "amqps://netinv@rabbitmq.netinv-data:5671/"
  vmURL:     "http://vmselect.netinv-data:8481/select/0/prometheus"
```

They are independent, so migrating one store at a time is a supported path
rather than an all-or-nothing switch. `values-prod.example.yaml` is this shape
with a pinned image digest, a real certificate and the flow service exposed.

The chart refuses to render a disabled store with no connection string, rather
than installing something that crash-loops.

## 4. Migrations

There is no migrations Job. `netinv-api` runs goose at startup under a Postgres
advisory lock, so replicas serialise and a chart upgrade is a schema upgrade —
one knob (NFR-51).

[Doc 19](19-kubernetes-design.md) originally specified a Helm pre-upgrade Job. That was dropped because the
api already did the work, and two migration paths is how a schema gets applied
twice in different orders.

`helm rollback` is safe one version back: migrations are
expand-migrate-contract, so the previous binary still runs against the new
schema.

## 5. Exposing the flow collector

`netinv-flow` is the only component that accepts unsolicited input from the
network — exporters push to it — and it defaults to `ClusterIP`, which no
exporter can reach. Nothing will report an error; flow will simply never arrive.

```yaml
services:
  flow:
    serviceType: LoadBalancer   # or NodePort
    env:
      # Without this, anything that can reach 2055/udp can write series.
      NETINV_FLOW_ALLOW: "198.51.100.1,198.51.100.2"
```

It is also the only single-replica service, and that is deliberate: UDP has no
replay, and NetFlow v9/IPFIX template state is per exporter, so a second replica
would see half of one exporter's datagrams and none of its templates. Scaling
means partitioning exporters across collectors, which needs its own design
([doc 34 §2](34-flow-collection.md)).

After a restart the collector holds no templates, so v9/IPFIX flow stays missing
for 10–20 minutes until exporters resend them. That window is counted as
`awaiting_template` rather than as an error, so nobody hunts a fault that is not
there.

## 6. Five things that will bite

Each of these was found by installing the chart, not by reading it.

### 6.1 A no-TLS Ingress breaks login, and looks like a broken application

Session cookies are `Secure` by default. Over plain HTTP the browser accepts the
login response, stores nothing, and sends no cookie back — login appears to
succeed and the very next request is a 401.

Set `ingress.tlsSecret`. The chart prints a warning on install when it sees this
combination. Only set `insecureCookies: true` for a deliberately plain-HTTP
deployment, and understand that it puts session tokens on the wire in clear.

### 6.2 A second release in the same cluster fails to install

PriorityClasses are cluster-scoped with fixed names, so release number two hits:

```
PriorityClass "netinv-data" ... exists and cannot be imported into the current
release: invalid ownership metadata
```

Set `priorityClasses.create=false` on every release after the first. The pods
still resolve the names the first release created.

### 6.3 Generated passwords, and rendering without a cluster

When you let the chart generate store passwords, it resolves each one once and
reads it back from its own Secret on later upgrades, so an upgrade does not hand
the application a new password for a database whose password never changed.

`lookup` returns nothing when there is no cluster to query — `helm template`,
`--dry-run`, and offline GitOps renderers all hit that path and generate a fresh
password each render. If you render manifests outside the cluster, **set
`data.postgres.password` and `data.rabbitmq.password` explicitly.**

(This is the area where the chart's worst bug lived: the generated password was
written into the Secret and into the DSN *in that same Secret* by two separate
evaluations, producing two different strings. Postgres initialised with one and
every client authenticated with the other. It rendered perfectly. CI now asserts
the two match.)

### 6.4 NetworkPolicy is off by default, and that is not laziness

On a cluster whose CNI does not enforce NetworkPolicy, the API server accepts
these objects and nothing enforces them — indistinguishable from a working
policy. Enabling it by default would hand operators a security control they only
believe they have.

Turn it on once you know your CNI enforces it, and set
`networkPolicy.deviceCIDRs`; the chart refuses to render a policy that would deny
the poller every device.

**ICMP cannot be expressed.** NetworkPolicy has no way to permit a protocol
without a port, so availability polling rides on the portless CIDR rule. Calico
and Cilium treat that as all-protocols; on CNIs that do not, ICMP is dropped
while SNMP keeps working — every device reports down over ping while its graphs
keep filling.

### 6.5 Retention is one setting read by two components

`retention` (default `2y`) configures VictoriaMetrics **and** the API's query
ceiling. The API *rejects* range queries beyond it rather than clamping them.
Set it below the store's real retention and the UI hides history you are paying
to keep; set it above and long timespans error instead of charting.

## 7. Remote-site pollers

Sites that are not the core cluster run the separate `netinv-poller` chart: a
poller Deployment plus a buffer PVC, pointed at the core's AMQP endpoint with an
enrolment token. The core tolerates one-version-old pollers ([doc 10 §3](10-connector-architecture.md)), so
upgrade the core first and the pollers last.

## 8. Verifying an install

Ready pods are not a working application. Check the things that fail silently:

```bash
# migrations actually applied
kubectl -n netinv exec netinv-postgres-0 -- \
  psql -U netinv -d netinv -tAc 'select max(version_id) from goose_db_version'

# the API answers, and carries the retention you configured
TOKEN=$(curl -sk -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<pw>"}' \
  https://netinv.your.domain/api/v1/auth/login | jq -r .access_token)
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://netinv.your.domain/api/v1/metrics/limits
# -> {"max_range_s":63072000,"poll_interval_s":60}   63072000 = 2y

# and a browser-style cookie login, which is what actually breaks without TLS
curl -sk -c jar -o /dev/null -w '%{http_code}\n' -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<pw>"}' \
  https://netinv.your.domain/api/v1/auth/login
curl -sk -b jar -o /dev/null -w '%{http_code}\n' -X POST \
  https://netinv.your.domain/api/v1/auth/refresh
```

If the second call 401s while the first returned 200, read §6.1.

## 9. Backups

The chart does not back anything up. `scripts/backup.sh` and `scripts/restore.sh`
target Docker containers; the Kubernetes equivalents are a CronJob around
`pg_dump` and a VictoriaMetrics snapshot.

Whatever you build, **rehearse the restore, and verify it by querying the data
rather than by the exit code**. The first real drill on this project found the
metrics half restoring nothing at all while exiting 0 and reporting healthy
([doc 20 §12.3](20-security-design.md)). Compare at a timestamp *inside* the backup window: a good
restore's newest sample is the snapshot instant, so an instant query at "now"
legitimately returns fewer series and looks like a failure.

## 10. Lab access without DNS

An Ingress host cannot be an IP address, so reaching a lab cluster by address
takes a catch-all Ingress with no `host:` alongside the chart's. Combined with a
TCP forwarder from the node's routable address to the ingress controller, that
gives you `https://<node>:8443` without touching DNS or a hosts file.

Do this only on a lab cluster. The same applies doubly to `kubectl proxy
--address=0.0.0.0 --accept-hosts=".*"` for the Kubernetes dashboard: it is
unauthenticated cluster-admin for anyone who can reach the port.
