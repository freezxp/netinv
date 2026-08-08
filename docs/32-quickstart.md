# 32 — Quick Start: Test-Run with Docker Compose

**Status:** draft · The easiest way to get a full NetInv running — the whole
platform on one host, no Kubernetes. Good for evaluation, demos, and small
single-site installs. For multi-site production use the Helm charts and the
[pilot runbook](31-pilot-runbook.md).

## Prerequisites

- **Docker** with the **Compose v2** plugin (`docker compose version` works).
- ~4 GB RAM free and ports `8090, 5672, 15672, 8428, 8025, 1161/udp` available.
- `git` and `openssl` (openssl ships with most systems).

That's it — no Go, Node, or database install needed. The images build from
source inside Docker.

## One command

```bash
git clone https://github.com/freezxp/netinv.git
cd netinv
./deploy/compose-app/quickstart.sh
```

The script:
1. generates secrets (vault master key, JWT key, DB passwords, a random admin
   password) into `deploy/compose-app/.env` — **once**; it reuses them on
   re-runs,
2. starts the data tier (PostgreSQL, Redis, RabbitMQ, VictoriaMetrics, an SNMP
   simulator, and MailHog),
3. builds and starts the six NetInv services + the web UI,
4. waits for readiness and prints your login.

First run builds the images (a few minutes); later runs are seconds.

When it finishes you'll see:

```
  NetInv is up.
    UI:        http://localhost:8090
    Username:  admin
    Password:  NetInv-xxxxxxxxxx
```

Open **http://localhost:8090**, log in, and change the admin password.

## First 5 minutes in the UI

The stack ships with an SNMP **simulator** so you can see real graphs without
any hardware.

1. **Platform → Credentials → Add** — name `sim`, kind `snmp_v2c`,
   community `public`.
2. **Platform → Sites** — a `Default Site` already exists; use it.
3. **Inventory → Add device**:
   - Name: `sim-switch`
   - Management IP: the SNMP simulator's container IP (find it with
     `docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' netinv-snmpsim-1`),
     or run the simulator on a routable address for a real test.
   - Site: Default Site · Credential: `sim` · SNMP port: `161`
4. Within ~2 minutes the device syncs (interfaces, LLDP) and **Dashboard** +
   **device detail** graphs begin to fill. Try the **Weathermaps** editor to
   place the node and draw a link.

> Real devices are simpler: just use their real management IP and SNMP
> credential — no simulator involved.

## What's running

| URL | Service |
|---|---|
| http://localhost:8090 | **NetInv UI** (nginx serving the app, proxying `/api`) |
| http://localhost:15672 | RabbitMQ management (user `netinv`) |
| http://localhost:8428 | VictoriaMetrics |
| http://localhost:8025 | MailHog — catches test emails so you can configure and test alert email without a real SMTP server |

```
docker ps --filter name=netinv        # see all containers
docker logs -f netinv-api-1           # follow a service
```

## Manage the stack

```bash
./deploy/compose-app/quickstart.sh          # start / update
./deploy/compose-app/quickstart.sh down     # stop and remove containers (keeps data)
./deploy/compose-app/quickstart.sh reset    # stop and WIPE all data (fresh start)
```

### After a host reboot

Every service runs with `restart: unless-stopped`, and Docker is enabled at boot on a normal install, so the stack comes back on its own. A deliberate `quickstart.sh down` still stays down — the policy only restores what was running.

Two things worth knowing if you ever bring it up by hand rather than via the script:

- The application services sit behind the **`app` compose profile**. A bare `docker compose up -d` starts only the data tier and looks like it succeeded; you need `--profile app`.
- The poller's state directory is a tmpfs pinned to `mode=1777`. Docker applies the tmpfs default mode when a container is *created* but not when an existing one is *started*, so without the explicit mode the poller dies on its first restart with `mkdir /var/lib/netinv-poller/buffer: permission denied`. Nothing else reports a problem when that happens — the API answers, the UI loads, the scheduler keeps queueing jobs — collection simply stops.

## Notes & going further

- **Secrets** live in `deploy/compose-app/.env` (git-ignored, `chmod 600`).
  The master key encrypts the credential vault — back it up; losing it means
  re-entering SNMP credentials (devices are unaffected, [doc 28](28-risk-assessment.md) R-10).
- **Not exposed to the internet as-is.** It serves plain HTTP on 8090 with
  `NETINV_INSECURE_COOKIES=1`. To expose it, put a TLS-terminating reverse
  proxy (Caddy/nginx/Traefik) in front, set `NETINV_UI_URL` to the HTTPS URL,
  and remove `NETINV_INSECURE_COOKIES` in the `.env`.
- **Bring your own data tier**: point the DSNs/passwords in
  `deploy/compose-app/docker-compose.deploy.yml` and `.env` at your managed
  PostgreSQL / RabbitMQ / VictoriaMetrics / Redis instead of the bundled
  containers.
- **Remote sites**: this single host runs one local poller. To monitor another
  datacenter, run the poller elsewhere and enroll it — see
  [doc 31 §4](31-pilot-runbook.md). The compose poller here polls whatever its
  host can reach on the network.
- **Scaling out** to Kubernetes when you outgrow one host: the same images and
  config drive the Helm charts in `deploy/helm/` — nothing to rebuild.
