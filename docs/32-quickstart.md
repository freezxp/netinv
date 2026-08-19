# 32 — Quick Start: Test-Run with Docker Compose

**Status:** draft · The easiest way to get a full NetInv running — the whole
platform on one host, no Kubernetes. Good for evaluation, demos, and small
single-site installs. For multi-site production use the Helm charts and the
[pilot runbook](31-pilot-runbook.md).

> Running this inside a **Proxmox LXC** container? See
> [doc 33](33-proxmox-lxc.md) first — three things behave differently there,
> and one of them (unprivileged ICMP) fails silently and partially.

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

### Upgrading an existing deployment

```bash
./deploy/compose-app/upgrade.sh                 # pull the latest, then deploy
./deploy/compose-app/upgrade.sh --no-pull       # deploy the working tree as it is
./deploy/compose-app/upgrade.sh --latest        # pull, and fail if it cannot
./deploy/compose-app/upgrade.sh --ref v1.1.0    # deploy a tag, branch or commit
./deploy/compose-app/upgrade.sh --dry-run       # print the plan, change nothing
./deploy/compose-app/upgrade.sh --recover       # stack is down: bring it back up
./deploy/compose-app/upgrade.sh --no-rollback   # leave a failure in place to inspect
```

**A plain run pulls first.** A deployment host's checkout should track the remote, and "deploy" almost always means "deploy what was pushed", so the script fetches, reports where the checkout sits against its upstream (`tracking origin/main: 2 behind, 0 ahead`), and fast-forwards before building.

The pull degrades rather than blocks. A stray edited file on the host, a detached checkout, or a branch holding local commits all stop the fast-forward — and none of them should stop an upgrade someone is running to fix something — so it says `NOT PULLING: <reason>`, states that it is deploying the tree as it stands, and carries on. `--latest` asks for the pull *explicitly* and therefore makes every one of those fatal instead: it is the form to use in automation, where deploying older code while reporting success is the failure worth preventing. `--no-pull` skips the pull entirely and rebuilds exactly what is checked out.

`--ref` takes a tag, a commit or a **branch**, and a branch name resolves to the *remote's* branch. That correction matters more than it looks: `git checkout main` after a fetch lands on the local `main`, which the fetch never moves, so asking for a branch by name used to deploy whatever it was when someone last pulled — and report success. Reproduced on a checkout two commits behind: it stayed two commits behind. Both `--latest` and a branch `--ref` are fast-forward only, so local commits the remote does not have stop the deploy loudly rather than being merged into an unreviewed build or reset away.

`quickstart.sh` also updates a stack it created, and for a plain quickstart host either works. `upgrade.sh` exists for the case quickstart cannot serve: **a stack whose compose invocation is not the one quickstart uses**. It takes no compose flags and assumes none — it reads the project name, the config files and the env file back off a running container, where Compose records all three as labels, and rebuilds exactly that. This matters because a NetInv stack is a multi-file project with an `--env-file`, and a bare `docker compose up -d api` resolves to the base file alone: that file carries no `NETINV_PG_DSN`, so the api comes back in skeleton mode with no database while postgres and rabbitmq are recreated with base-file credentials, and every other service then fails AMQP auth. Nothing is lost — the data is in volumes — but the stack is down until someone works out why.

**It checks disk before doing any work**, because both things it needs space for fail unhelpfully when they run out. A Go build that fills the filesystem dies inside BuildKit as `exit code 1`, with the real message — `no space left on device` — buried in one of eight parallel build streams; that cost a real deployment several rounds of diagnosis. The check asks Docker where its root directory actually is rather than assuming `/var/lib/docker`, **and checks containerd's root as well when the containerd snapshotter is in use** — which is the default on current Docker installs. That distinction is the whole point: with that snapshotter, image layers and build cache live under `/var/lib/containerd`, and `DockerRootDir` stays nearly empty. On the host that hit this, `/var/lib/docker` was a 128 GB mount with 127 GB free while `/` — holding containerd — was 100% full, so a check of `DockerRootDir` alone was a reassuring answer to the wrong question, and would have passed on the one machine that could not build. Override the location with `CONTAINERD_ROOT` if containerd is configured with a non-default root this cannot read. Inodes are checked too, since a Go build writes enough small files to exhaust them while `df -h` still looks fine. Thresholds are `MIN_BUILD_MB` and `MIN_BACKUP_MB`; the backup requirement is estimated from the size of the newest existing backup, which predicts the next one better than any fixed number.

**After a verified deploy it clears the build cache** (`docker builder prune -af`, plus dangling images), because that is what fills the disk: on the pilot the cache reached 34 GB against 40 GB of images and took the root filesystem to 94%, which then blocked the next upgrade. The cost is honest — the next build starts cold, a few minutes on this stack — so `BUILD_CACHE_KEEP=10GB` switches to a bounded prune that keeps recent layers and stays warm, and `--no-prune` skips it entirely.

Two lines it does not cross. It prunes **after** verification, never before: pruning first would clear the cache the build about to run could have used, and pruning on the way out of a failure would destroy the evidence. And it removes only **dangling** images, never `image prune -af` — that would delete the previous release's images, which are exactly what the rollback path starts the stack from when a recreate fails.

It backs up first (`scripts/backup.sh`, skip with `--skip-backup`), then **prunes to the newest `--keep N` backups, 3 by default** — this script is the reason there are many, since it takes a full dump plus a metrics snapshot on every single run, and left alone they accumulate on whatever filesystem `BACKUP_DIR` points at until it fills. Only directories named like the timestamps `backup.sh` generates are ever removed, so pointing `BACKUP_DIR` at a directory holding anything else cannot lose it. `--keep 0` disables pruning. `scripts/backup.sh` takes the same retention as an opt-in `KEEP=N`, left off by default so an existing cron entry keeps behaving as it did.

Note that `BACKUP_DIR` defaults to `backups/` inside the checkout, which is often the root filesystem. On a host with a small root and a large data disk, point it at the large one: `BACKUP_DIR=/data/netinv-backups ./deploy/compose-app/upgrade.sh`.

It builds before it recreates anything so a compile error costs only time, waits for healthchecks, and then verifies two things worth verifying:

- **The schema advanced to the highest migration in the checkout.** Migrations are embedded in the api binary, so a new `backend/migrations/*.sql` does nothing until the image is rebuilt. A database version behind the tree is how a stale image announces itself — the same fault that reads as `goose: no migrations to run` while the file sits on disk.
- **The UI answers through the frontend proxy**, which exercises nginx, the api and the database in one request.

**It rolls itself back.** If the build fails, or the services do not come up healthy, the script restores the checkout it started from and brings the stack back up on the images already present, rather than leaving a half-upgraded host down while someone reads the output. Two details decide what that rollback is actually worth:

- **A failed build is a clean rollback.** Compose builds into new images and only swaps them in at `up`, so a build that fails has not touched the running stack — the previous images are still tagged and the stack returns to exactly where it was. This is the common case, since a compile error is the likeliest failure.
- **A failed recreate is not.** By then the new images are built and tagged under the same names, so what comes back up is the *new* code, not the old. The script says so rather than reporting a rollback it did not perform. Getting the old binaries back from there means checking out the previous commit and re-running.

**No rollback touches the database.** goose does not roll a migration back, so if the new api started long enough to migrate, the code is rolled back *under* a newer schema. The schema is a superset and nothing is lost, but old code is then serving data it does not fully know about, and the script prints the query to check the schema version against the highest migration in the tree. To undo the data as well, restore from the backup with `scripts/restore.sh` (doc 20 §12.3) and read its `--force` warning first.

`--recover` is the same machinery with everything optional removed: no backup, no build, no checkout change — it locates the stack from its container labels (**including stopped ones**, which is the point) and starts what is already built. It is for the case where a host is down and the priority is service, not version. `--no-rollback` suppresses the automatic recovery when you would rather inspect the wreckage than have it cleaned up.

Images are built locally rather than pulled. The GHCR packages are private, so an anonymous pull of `ghcr.io/freezxp/netinv-*` gets a 401; "images published per release" is true only for someone holding a token.

### After a host reboot

Every service runs with `restart: unless-stopped`, and Docker is enabled at boot on a normal install, so the stack comes back on its own. A deliberate `quickstart.sh down` still stays down — the policy only restores what was running.

Two things worth knowing if you ever bring it up by hand rather than via the script:

- The application services sit behind the **`app` compose profile**. A bare `docker compose up -d` starts only the data tier and looks like it succeeded; you need `--profile app`.
- The poller's state directory is a tmpfs pinned to `mode=1777`. Docker applies the tmpfs default mode when a container is *created* but not when an existing one is *started*, so without the explicit mode the poller dies on its first restart with `mkdir /var/lib/netinv-poller/buffer: permission denied`. Nothing else reports a problem when that happens — the API answers, the UI loads, the scheduler keeps queueing jobs — collection simply stops.

## Notes & going further

- **Secrets** live in `deploy/compose-app/.env` (git-ignored, `chmod 600`).
  The master key encrypts the credential vault — back it up; losing it means
  re-entering SNMP credentials (devices are unaffected, [doc 28](28-risk-assessment.md) R-10).
- **Metrics are kept for 2 years by default** (`NETINV_VM_RETENTION` in
  `deploy/compose-app/.env`), so the full range selector works immediately.
  Budget roughly 75 MB per device per year — a handful of devices is a couple
  of GB, 500 devices is ~75 GB. Lower it if the disk is small; VictoriaMetrics
  stops accepting writes when the disk fills, and collection stops with it.
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
