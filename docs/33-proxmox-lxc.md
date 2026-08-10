# 33 — Running NetInv in a Proxmox LXC container

**Status:** draft · **Depends on:** 32 · Deploying the Compose stack inside an
LXC container on Proxmox VE, rather than on a VM or bare metal.

> **Verified on hardware, end to end.** Everything here except §4.1 was run on
> Proxmox VE 9.2.10 with a Debian 13 unprivileged container and Docker 26.1.5:
> the `pct` flags, the package names, the ICMP behaviour and its fix. The
> resulting install collected ICMP from a real gateway — `netinv_icmp_up = 1`,
> RTT 0.18 ms, no permission errors — which is the check that would have failed
> before the fix in §4.2. §4.1 (ZFS storage driver) was not exercised: that host
> uses LVM-thin, where `overlay2` works. If you run
> a ZFS rootfs, §6's first check is the one to watch, and please
> [report what you get](https://github.com/freezxp/netinv/issues/new/choose).

## 1. Should you use LXC at all?

An LXC container is lighter than a VM and shares the host kernel, which is why
it is attractive for a monitoring stack that mostly waits on the network. The
trade is that Docker-inside-LXC is a nested-container arrangement Proxmox
supports but does not go out of its way to make easy, and two things bite:
the storage driver and anything needing kernel privileges.

If you want the least surprising path, **a Proxmox VM running Debian is the
boring choice** and the quickstart in [doc 32](32-quickstart.md) applies with
no changes. Use LXC when the density matters to you and you are willing to read
§4.

There is no NetInv-specific reason to prefer one. Nothing in the product needs
a dedicated kernel.

## 2. Sizing

The pilot — 13 devices, 2507 series, four gateways in an SD-WAN mesh — runs
comfortably in:

| Resource | Minimum | Comfortable |
|---|---|---|
| Cores | 2 | 4 |
| RAM | 4 GB | 8 GB |
| Disk | 20 GB | 40 GB + growth |

Disk is the one that grows. Metrics cost roughly **75 MB per device per year**
at the default 60-second cadence (measured, not estimated — see
[doc 04 §4](04-nfr.md)), and retention defaults to 2 years. Platform → Capacity
reports what your disk actually sustains against what retention asks for; check
it after a week rather than guessing now.

## 3. Creating the container

There is a helper script. Run it on the Proxmox host:

```bash
curl -fsSL https://raw.githubusercontent.com/freezxp/netinv/main/deploy/proxmox/netinv-lxc.sh -o netinv-lxc.sh
chmod +x netinv-lxc.sh
./netinv-lxc.sh create
```

It picks a free CTID, finds a Debian template you already have, creates the
container with the right features, installs Docker, clones the repo, runs the
quickstart, checks the things LXC changes, and prints the URL and password.
Roughly ten minutes, most of it building images.

Everything is overridable:

```bash
CTID=130 HOSTNAME_=netinv-lab MEMORY=2048 DISK=20 STORAGE=local-lvm \
  BRIDGE=vmbr0 IPCONF=dhcp BRANCH=main ./netinv-lxc.sh create
```

A static address instead of DHCP: `IPCONF='10.0.0.50/24,gw=10.0.0.1'`.

Two more subcommands:

```bash
./netinv-lxc.sh verify 120     # re-run the LXC-specific checks
./netinv-lxc.sh destroy 120    # stop and remove it entirely
```

### 3.1 By hand

If you would rather not run someone else's script, this is all `create` does
that `pct` does not do for you:

```bash
pct create 120 local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst \
  --hostname netinv \
  --cores 4 --memory 4096 --swap 2048 \
  --rootfs local-lvm:40 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp \
  --features nesting=1,keyctl=1 \
  --unprivileged 1 \
  --onboot 1 --start 1
```

The two `--features` are not optional:

- **`nesting=1`** lets a container run containers. Without it Docker will not
  start; it needs its own cgroup and procfs views.
- **`keyctl=1`** gives the container its own kernel keyring. Docker uses
  `keyctl` while unpacking images, so without it the failure appears during a
  pull and reads as a broken registry rather than a missing feature.

**`--unprivileged 1` is deliberate.** A privileged container would make §4.2 go
away by handing out near-host capabilities, which is the wrong trade for a box
that ends up holding SNMP credentials for network equipment. Everything below
works unprivileged.

`local-lvm:40` assumes LVM-thin storage. On ZFS read §4.1 first.

## 4. The three things that actually differ from a VM

### 4.1 Docker's storage driver on ZFS

Docker's default `overlay2` needs a filesystem that supports overlay upper
layers. On an **ext4 or LVM-thin** rootfs this works inside an unprivileged
LXC. On a **ZFS-backed** LXC rootfs, `overlay2` is typically unavailable and
Docker falls back to `vfs`, which copies every layer rather than sharing it —
functional, but images take several times the space and builds are slow.

Options, best first:

1. Give the container an ext4 or LVM-thin rootfs (`--rootfs local-lvm:40`).
2. Mount a separate ext4 volume at `/var/lib/docker` and leave the rest on ZFS.
3. Accept `vfs` and size the disk generously. NetInv builds seven images; this
   is the option that turns 20 GB into not enough.

Check which you got, inside the container:

```bash
docker info --format '{{.Driver}}'
```

### 4.2 ICMP availability checks — the one that bites

This fails **silently and partially**: SNMP keeps working, graphs keep filling,
and only availability goes wrong — the same shape as the two collection
outages in this project's history. It is also the one thing here that does
*not* behave the way it does on a normal Docker host.

NetInv's poller runs as a **non-root** user (uid 65532) and uses *unprivileged*
ICMP — `SOCK_DGRAM` sockets, not raw ones. The kernel permits those only for
GIDs inside `net.ipv4.ping_group_range`, which is namespaced per network
namespace.

On an ordinary Docker host this is invisible: Docker writes
`0 2147483647` into every container's netns, so any uid may ping. Measured on
one such host, the *host* value was `1 0` — an empty range, unprivileged ping
disabled — and containers pinged fine regardless.

**Inside an unprivileged LXC it does not work.** `2147483647` is not a
mappable GID in the container's user namespace, so Docker's write is clamped,
and each container ends up with:

```
net.ipv4.ping_group_range = 65534	65534
```

A range of exactly one GID — and not the poller's 65532. Measured, in order:

| Attempt | Result |
|---|---|
| Poller uid 65532, unprivileged ICMP | **denied** |
| `sysctl -w …="0 2147483647"` inside the LXC | `Invalid argument` — not mappable |
| `sysctl -w …="0 65534"` inside the LXC | succeeds, but Docker overrides it per container |
| `--cap-add=NET_RAW` + raw sockets as non-root | **denied** — the capability is dropped for a non-root uid in a userns |
| **`--sysctl net.ipv4.ping_group_range="0 65534"` on the container** | **works** |

The fix is therefore a per-container sysctl, and it is already in
`deploy/compose-app/docker-compose.deploy.yml`:

```yaml
poller:
  sysctls:
    net.ipv4.ping_group_range: "0 65534"
```

Nothing to do — a clone of this repo deploys correctly in an LXC. It is
harmless on a normal Docker host, where uids sit far below 65534 anyway.

Two approaches that look reasonable and are not:

- **Setting the sysctl on the Proxmox host or inside the LXC.** Docker rewrites
  the value in each container's netns when the container starts, so the outer
  value is discarded.
- **Granting `NET_RAW` and switching to `NETINV_ICMP_PRIVILEGED=1`.** In an
  unprivileged userns the capability is not effective for a non-root uid, so
  raw sockets are still refused — and it would be more authority than
  availability checking needs even if it worked.

### 4.3 SNMP is fine

SNMP is ordinary outbound UDP/161 and needs no capability, no privileged port
and no host configuration. If SNMP works from the LXC's shell (`snmpwalk`), it
works from the poller.

## 5. Installing NetInv by hand

`netinv-lxc.sh create` already does this. If you built the container yourself:

```bash
apt update && apt install -y docker.io git
apt install -y docker-compose-v2 || apt install -y docker-compose
git clone https://github.com/freezxp/netinv.git && cd netinv
./deploy/compose-app/quickstart.sh
```

The compose package was renamed between Debian releases: **bookworm** has
`docker-compose-v2`, **trixie** has `docker-compose` (which is v2). Asking for
the wrong one fails with "Unable to locate package", which looks like a broken
mirror. The `||` above covers both.

The quickstart generates secrets, builds the images, starts the stack and
prints the admin password. The UI is on port **8090**.

Two settings worth revisiting before you point it at real equipment:

- `NETINV_VM_RETENTION` in `deploy/compose-app/.env` — 2 years by default.
- The polling cadence, from Platform → Pollers, if your switches rate-limit
  SNMP. Some do: the pilot has UniFi switches that answer a single `snmpget`
  instantly and then stop responding partway through a walk.

## 6. Verifying the deployment

Run these inside the container after the stack is up. Each one checks something
§4 says could differ in LXC.

```bash
docker info --format 'storage driver: {{.Driver}}'      # want overlay2, not vfs
docker run --rm alpine cat /proc/sys/net/ipv4/ping_group_range   # "65534 65534" is normal in LXC
docker ps --filter name=netinv --format '{{.Names}} {{.Status}}' # 13 containers up
curl -sf localhost:8090/ >/dev/null && echo "UI ok"
```

Then, from the UI, the two that prove collection actually works end to end:

- **Inventory** — add a device and confirm it leaves `pending`.
- **Dashboard** — after ~2 minutes, bandwidth and latency should both draw. If
  bandwidth draws and latency does not, ICMP is the problem: confirm the poller
  actually got its sysctl with
  `docker inspect netinv-poller-1 --format '{{.HostConfig.Sysctls}}'`, which
  should show `0 65534`.

Availability failing alone while SNMP succeeds is the signature of the
`ping_group_range` issue, and it is worth knowing that shape in advance —
everything else on the page will look healthy.

## 7. Using it as a throwaway test environment

`create` and `destroy` exist as a pair because the most useful thing about an
LXC here is that it costs about ten minutes to have a complete NetInv and one
command to have none.

```bash
./netinv-lxc.sh create                 # ~10 min, prints URL and password
# … point it at real devices, break things, try an upgrade …
./netinv-lxc.sh destroy 121            # gone, including its volumes
```

Why this is worth doing rather than testing against your live instance:

- **It polls real hardware.** Four of the seven connectors have never met a
  real device ([doc 10](10-connector-architecture.md)), and every connector
  that has met one needed corrections no MIB reading would have produced. A
  throwaway instance on the same network as the equipment is the cheapest way
  to find the next one, and if it misbehaves you delete it.
- **It cannot damage the instance you rely on.** NetInv holds SNMP credentials
  and an append-only audit log. A test that writes into the deployment you
  trust leaves marks you cannot remove — this project has already done that
  once, with integration tests pointed at a live database.
- **It is a clean-room upgrade rehearsal.** `BRANCH=` takes any branch, so a
  change can be run end to end against real devices before it reaches the
  instance people look at.

Useful shapes:

```bash
# small, for a quick check
MEMORY=2048 DISK=20 HOSTNAME_=netinv-test ./netinv-lxc.sh create

# a branch, to rehearse a change against real hardware
BRANCH=my-connector-fix HOSTNAME_=netinv-pr ./netinv-lxc.sh create

# several at once — CTIDs and hostnames are picked automatically
./netinv-lxc.sh create && ./netinv-lxc.sh create
```

Two things to remember. A test instance polls **real equipment**, so it adds
SNMP and ICMP load to devices someone else may be relying on — the pilot has
UniFi switches that stop answering walks under repeated querying. And it needs
credentials for those devices, which makes a throwaway container a place real
secrets end up; `destroy --purge` removes the volumes, but treat the container
as sensitive while it exists.

If you record a connector fixture from a device while testing, use
`scripts/record-fixture.sh` — it redacts serial numbers, addresses and MACs at
capture time, including the ones hidden in table indices.

## 8. Backups

An LXC snapshot captures the whole container including the Docker volumes, and
is the easiest option. It is not sufficient on its own: `NETINV_MASTER_KEY`
lives in `deploy/compose-app/.env` inside the same container, so a snapshot
holds the vault and its key together. Anyone who can restore the snapshot has
the SNMP credentials. Keep a copy of that key somewhere else, and treat the
snapshot store as sensitive ([doc 28](28-risk-assessment.md) R-10).
