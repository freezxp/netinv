# 33 — Running NetInv in a Proxmox LXC container

**Status:** draft · **Depends on:** 32 · Deploying the Compose stack inside an
LXC container on Proxmox VE, rather than on a VM or bare metal.

> **What is verified and what is not.** The container-level behaviour described
> in §4 was measured on a running NetInv deployment: Docker's handling of
> `ping_group_range`, the poller's non-root user, and the ICMP path. The
> Proxmox-specific parts — `pct` flags, ZFS storage-driver behaviour — are from
> Proxmox and Docker documentation and have **not** been run against a Proxmox
> host by the author. §6 is a checklist for confirming them on yours; please
> [report what actually happens](https://github.com/freezxp/netinv/issues/new/choose).

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

Run on the **Proxmox host**, not inside the container:

```bash
pct create 120 local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst \
  --hostname netinv \
  --cores 4 --memory 8192 --swap 2048 \
  --rootfs local-lvm:40 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp \
  --features nesting=1,keyctl=1 \
  --unprivileged 1 \
  --onboot 1 \
  --start 1
```

The two `--features` are not optional:

- **`nesting=1`** lets a container run containers. Without it Docker fails at
  startup; it needs to mount its own cgroup and procfs views.
- **`keyctl=1`** gives the container its own kernel keyring. Docker uses
  `keyctl` during image unpacking, and an unprivileged container without this
  fails there rather than at daemon start — which reads as a broken pull.

**`--unprivileged 1` is deliberate.** A privileged LXC makes some of §4 go away
by giving the container near-host capabilities, and that is precisely why it is
the wrong default for a box holding SNMP credentials for your network
equipment. Everything below works unprivileged.

`local-lvm:40` assumes LVM-thin storage. On ZFS see §4.1 before choosing.

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

### 4.2 ICMP availability checks

This is the one most likely to bite, because it fails **silently and
partially**: SNMP keeps working, graphs keep filling, and only availability
goes wrong — which is the same shape as the two collection outages recorded in
this project's history.

NetInv's poller runs as a **non-root** user and by default uses *unprivileged*
ICMP — UDP sockets of type `SOCK_DGRAM`, not raw sockets. The kernel allows
those only for GIDs inside `net.ipv4.ping_group_range`, which is namespaced per
network namespace.

Measured on a working deployment: the Docker host had
`net.ipv4.ping_group_range = 1 0` — an empty range, unprivileged ping disabled
— and ICMP still worked, because **Docker sets `0 2147483647` inside each
container's network namespace** regardless of the host value. That is what the
poller relies on, and it is why nothing on the host needs changing.

Inside an unprivileged LXC, Docker is root in the container's user namespace
and the sysctl is namespaced, so the same write should succeed. Confirm rather
than assume:

```bash
docker run --rm alpine cat /proc/sys/net/ipv4/ping_group_range
# want: 0	2147483647
```

If it reads `1 0`, unprivileged ping is unavailable and every device will
report down over ICMP while SNMP looks fine. Two fixes:

```bash
# On the Proxmox host, for that container's namespace:
sysctl -w net.ipv4.ping_group_range="0 2147483647"
```

or switch the poller to raw sockets, which needs a capability:

```yaml
# deploy/compose-app/docker-compose.deploy.yml, poller service
environment:
  NETINV_ICMP_PRIVILEGED: "1"
cap_add: [NET_RAW]
```

Prefer the first. `NET_RAW` lets the poller construct arbitrary packets, which
is more authority than availability checking needs.

### 4.3 SNMP is fine

SNMP is ordinary outbound UDP/161 and needs no capability, no privileged port
and no host configuration. If SNMP works from the LXC's shell (`snmpwalk`), it
works from the poller.

## 5. Installing NetInv

Inside the container, exactly as [doc 32](32-quickstart.md):

```bash
apt update && apt install -y docker.io docker-compose-v2 git
git clone https://github.com/freezxp/netinv.git && cd netinv
./deploy/compose-app/quickstart.sh
```

The script generates secrets, builds the images, starts the stack and prints
the admin password. The UI is on port **8090**.

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
docker run --rm alpine cat /proc/sys/net/ipv4/ping_group_range   # want 0 2147483647
docker ps --filter name=netinv --format '{{.Names}} {{.Status}}' # 13 containers up
curl -sf localhost:8090/ >/dev/null && echo "UI ok"
```

Then, from the UI, the two that prove collection actually works end to end:

- **Inventory** — add a device and confirm it leaves `pending`.
- **Dashboard** — after ~2 minutes, bandwidth and latency should both draw. If
  bandwidth draws and latency does not, ICMP is the problem: go back to §4.2.

Availability failing alone while SNMP succeeds is the signature of the
`ping_group_range` issue, and it is worth knowing that shape in advance —
everything else on the page will look healthy.

## 7. Backups

An LXC snapshot captures the whole container including the Docker volumes, and
is the easiest option. It is not sufficient on its own: `NETINV_MASTER_KEY`
lives in `deploy/compose-app/.env` inside the same container, so a snapshot
holds the vault and its key together. Anyone who can restore the snapshot has
the SNMP credentials. Keep a copy of that key somewhere else, and treat the
snapshot store as sensitive ([doc 28](28-risk-assessment.md) R-10).
