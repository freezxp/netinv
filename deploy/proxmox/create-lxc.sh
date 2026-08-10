#!/usr/bin/env bash
# Create a Proxmox LXC container suitable for running NetInv.
#
# Run this on the Proxmox VE host, not inside a container. It only creates and
# starts the container — installing NetInv is the quickstart, printed at the
# end. Doc 33 explains why each flag is here.
#
# The defaults are sized for the reference pilot (13 devices) with headroom.
# Metrics cost roughly 75 MB per device per year at a 60-second cadence, and
# retention defaults to two years, so disk is the dimension to revisit.
set -euo pipefail

CTID="${CTID:-120}"
HOSTNAME="${HOSTNAME_:-netinv}"
CORES="${CORES:-4}"
MEMORY="${MEMORY:-8192}"
SWAP="${SWAP:-2048}"
DISK="${DISK:-40}"
STORAGE="${STORAGE:-local-lvm}"
BRIDGE="${BRIDGE:-vmbr0}"
IPCONF="${IPCONF:-dhcp}"
TEMPLATE="${TEMPLATE:-}"

command -v pct >/dev/null || {
	printf 'pct not found — run this on the Proxmox VE host, not in a container.\n' >&2
	exit 1
}

if pct status "$CTID" >/dev/null 2>&1; then
	printf 'CTID %s already exists. Set CTID=<free id> and re-run.\n' "$CTID" >&2
	exit 1
fi

# Pick the newest Debian template already downloaded, rather than guessing a
# version string that ages badly.
if [ -z "$TEMPLATE" ]; then
	TEMPLATE=$(pveam list local 2>/dev/null |
		awk '/debian-1[2-9]-standard/ {print $1}' | sort -V | tail -1 || true)
fi
if [ -z "$TEMPLATE" ]; then
	printf 'No Debian template found. Download one first, e.g.:\n' >&2
	printf '  pveam update && pveam available --section system | grep debian\n' >&2
	printf '  pveam download local debian-12-standard_12.7-1_amd64.tar.zst\n' >&2
	printf 'Then re-run, or set TEMPLATE=<volid>.\n' >&2
	exit 1
fi

printf 'Creating CT %s (%s)\n' "$CTID" "$HOSTNAME"
printf '  template : %s\n' "$TEMPLATE"
printf '  resources: %s cores, %s MB RAM, %s GB on %s\n' "$CORES" "$MEMORY" "$DISK" "$STORAGE"
printf '  network  : %s on %s\n\n' "$IPCONF" "$BRIDGE"

# nesting: Docker cannot start without it — it needs its own cgroup and procfs
#          views inside the container.
# keyctl : Docker uses the kernel keyring while unpacking images. Without it an
#          unprivileged container fails during pull, not at daemon start, which
#          reads as a broken registry rather than a missing feature.
# unprivileged: deliberate. A privileged container would paper over some of
#          this by handing out near-host capabilities, which is the wrong
#          trade for a host holding SNMP credentials for network equipment.
pct create "$CTID" "$TEMPLATE" \
	--hostname "$HOSTNAME" \
	--cores "$CORES" --memory "$MEMORY" --swap "$SWAP" \
	--rootfs "${STORAGE}:${DISK}" \
	--net0 "name=eth0,bridge=${BRIDGE},ip=${IPCONF}" \
	--features nesting=1,keyctl=1 \
	--unprivileged 1 \
	--onboot 1 \
	--start 1

printf '\nWaiting for the container to finish booting'
for _ in $(seq 1 30); do
	if pct exec "$CTID" -- test -d /run/systemd/system 2>/dev/null; then break; fi
	printf '.'
	sleep 1
done
printf '\n\n'

cat <<EOF
Container $CTID is up. Next, inside it:

  pct enter $CTID
  apt update && apt install -y docker.io docker-compose git   # trixie; bookworm: docker-compose-v2
  git clone https://github.com/freezxp/netinv.git && cd netinv
  ./deploy/compose-app/quickstart.sh

Then verify the two things that behave differently under LXC (doc 33 §6):

  docker info --format '{{.Driver}}'
      want overlay2. "vfs" means the rootfs cannot host overlay layers —
      usually ZFS — and images will take several times the space.

  docker inspect netinv-poller-1 --format '{{.HostConfig.Sysctls}}'
      want "map[net.ipv4.ping_group_range:0 65534]". Inside an unprivileged
      LXC, Docker clamps its usual ping_group_range to "65534 65534" — one gid,
      and not the poller's — so unprivileged ICMP is refused. The compose file
      sets the range explicitly, which is what makes availability work here.
      Without it every device reports down over ICMP while SNMP keeps working
      and the rest of the UI looks healthy. Doc 33 §4.2.
EOF
