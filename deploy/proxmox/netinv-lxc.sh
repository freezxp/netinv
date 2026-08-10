#!/usr/bin/env bash
# NetInv in a Proxmox LXC container — create, verify, destroy.
#
# Run on the Proxmox VE host. `create` goes from nothing to a working NetInv
# with its credentials printed; `destroy` removes it completely. That pairing is
# the point: a throwaway instance is the cheapest way to test a change against a
# real network without touching the one you rely on.
#
# Everything here was derived by doing it (doc 33). The parts that are not
# obvious — nesting and keyctl, the Debian-release package split, and the
# unprivileged-LXC ICMP clamp — are the parts that cost an afternoon to find.
set -euo pipefail

ACTION="${1:-create}"
[ $# -gt 0 ] && shift || true

CTID="${CTID:-}"
HOSTNAME_="${HOSTNAME_:-netinv}"
CORES="${CORES:-4}"
MEMORY="${MEMORY:-4096}"
SWAP="${SWAP:-2048}"
DISK="${DISK:-40}"
STORAGE="${STORAGE:-local-lvm}"
BRIDGE="${BRIDGE:-vmbr0}"
IPCONF="${IPCONF:-dhcp}"
TEMPLATE="${TEMPLATE:-}"
REPO="${REPO:-https://github.com/freezxp/netinv.git}"
BRANCH="${BRANCH:-main}"

die() {
	printf '\n%s\n' "$*" >&2
	exit 1
}
say() { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }

command -v pct >/dev/null || die "pct not found — run this on the Proxmox VE host, not in a container."

# ---------------------------------------------------------------- helpers ---

# next_ctid finds a free id. VMs and containers share the id space, so both
# must be consulted — picking one that a stopped VM owns fails at create time
# with a message that does not mention the VM.
next_ctid() {
	local used id
	used=$( (pct list 2>/dev/null | awk 'NR>1{print $1}'; qm list 2>/dev/null | awk 'NR>1{print $1}') | sort -n)
	for id in $(seq 120 199); do
		grep -qx "$id" <<<"$used" || {
			printf '%s' "$id"
			return
		}
	done
	die "No free CTID between 120 and 199."
}

pick_template() {
	local t
	t=$(pveam list local 2>/dev/null | awk '/debian-1[3-9]-standard/ {print $1}' | sort -V | tail -1)
	[ -z "$t" ] && t=$(pveam list local 2>/dev/null | awk '/debian-12-standard/ {print $1}' | sort -V | tail -1)
	printf '%s' "$t"
}

in_ct() { pct exec "$CTID" -- bash -lc "$1"; }

# --------------------------------------------------------------- destroy ----

if [ "$ACTION" = "destroy" ]; then
	CTID="${1:-${CTID:-}}"
	[ -n "$CTID" ] || die "usage: $0 destroy <ctid>"
	pct status "$CTID" >/dev/null 2>&1 || die "CT $CTID does not exist."
	name=$(pct config "$CTID" | awk -F': ' '/^hostname:/{print $2}')
	say "Destroying CT $CTID ($name) and everything in it."
	pct stop "$CTID" >/dev/null 2>&1 || true
	pct destroy "$CTID" --purge
	say "Gone."
	exit 0
fi

# ---------------------------------------------------------------- verify ----

verify() {
	local fail=0 driver sysctls icmp

	driver=$(in_ct "docker info --format '{{.Driver}}' 2>/dev/null" || true)
	if [ "$driver" = "overlay2" ]; then
		say "  ok    storage driver: overlay2"
	else
		say "  WARN  storage driver: ${driver:-unknown} — not overlay2."
		say "        Usually a ZFS rootfs. Images will take several times the"
		say "        space and builds will be slow. Doc 33 §4.1."
	fi

	sysctls=$(in_ct "docker inspect netinv-poller-1 --format '{{.HostConfig.Sysctls}}' 2>/dev/null" || true)
	case "$sysctls" in
	*ping_group_range:0\ 65534*) say "  ok    poller ICMP sysctl: $sysctls" ;;
	*) say "  FAIL  poller ICMP sysctl missing (got '${sysctls:-none}') — doc 33 §4.2"; fail=1 ;;
	esac

	# The one that matters. Inside an unprivileged LXC, Docker clamps
	# ping_group_range to a single gid that is not the poller's, and
	# availability silently stops working while SNMP carries on.
	icmp=$(in_ct "curl -s http://localhost:8428/api/v1/query --data-urlencode 'query=count(netinv_icmp_up)' \
		| python3 -c 'import json,sys; r=json.load(sys.stdin)[\"data\"][\"result\"]; print(r[0][\"value\"][1] if r else 0)'" 2>/dev/null || echo 0)
	if [ "${icmp:-0}" -gt 0 ] 2>/dev/null; then
		say "  ok    ICMP collection: $icmp target(s) reporting"
	else
		say "  ..    ICMP collection: nothing yet — normal until a device is added"
		say "        and polled. Re-run '$0 verify $CTID' a couple of minutes after."
	fi

	if in_ct "curl -sf http://localhost:8090/ >/dev/null"; then
		say "  ok    UI answering on :8090"
	else
		say "  FAIL  UI not answering on :8090"
		fail=1
	fi
	return $fail
}

if [ "$ACTION" = "verify" ]; then
	CTID="${1:-${CTID:-}}"
	[ -n "$CTID" ] || die "usage: $0 verify <ctid>"
	pct status "$CTID" >/dev/null 2>&1 || die "CT $CTID does not exist."
	step "Checking CT $CTID"
	verify
	exit $?
fi

[ "$ACTION" = "create" ] || die "usage: $0 [create|verify <ctid>|destroy <ctid>]"

# ----------------------------------------------------------------- create ---

[ -n "$CTID" ] || CTID=$(next_ctid)
pct status "$CTID" >/dev/null 2>&1 && die "CTID $CTID is taken. Set CTID=<free id>."

[ -n "$TEMPLATE" ] || TEMPLATE=$(pick_template)
[ -n "$TEMPLATE" ] || die "No Debian template found. Download one first:
  pveam update
  pveam available --section system | grep debian
  pveam download local debian-13-standard_13.1-2_amd64.tar.zst"

# Memory is the resource most likely to be oversubscribed on a host that is
# already doing something. Warn rather than refuse — the operator knows their
# overcommit tolerance better than this script does.
avail=$(free -m | awk 'NR==2{print $7}')
if [ "$avail" -lt "$MEMORY" ]; then
	say "NOTE: host has ${avail} MB available but MEMORY=${MEMORY}."
	say "      NetInv runs in 4096; set MEMORY=2048 for a small test instance."
fi

step "Creating CT $CTID ($HOSTNAME_)"
say "  template : $TEMPLATE"
say "  resources: $CORES cores, $MEMORY MB RAM, $DISK GB on $STORAGE"
say "  network  : $IPCONF on $BRIDGE"

# nesting: Docker cannot start without it.
# keyctl : Docker needs the kernel keyring while unpacking images. Without it
#          the failure appears during pull, which reads as a broken registry.
# unprivileged: deliberate — this box ends up holding SNMP credentials for
#          network equipment, and a privileged container hands out near-host
#          capabilities to avoid problems that turn out to be solvable anyway.
pct create "$CTID" "$TEMPLATE" \
	--hostname "$HOSTNAME_" \
	--cores "$CORES" --memory "$MEMORY" --swap "$SWAP" \
	--rootfs "${STORAGE}:${DISK}" \
	--net0 "name=eth0,bridge=${BRIDGE},ip=${IPCONF}" \
	--features nesting=1,keyctl=1 \
	--unprivileged 1 \
	--onboot 1 \
	--start 1 >/dev/null

step "Waiting for the container to boot and get an address"
ip=""
for _ in $(seq 1 60); do
	ip=$(pct exec "$CTID" -- ip -4 -o addr show eth0 2>/dev/null | awk '{print $4}' | cut -d/ -f1 || true)
	[ -n "$ip" ] && break
	sleep 1
done
[ -n "$ip" ] || die "No address after 60s. With ip=dhcp, check a DHCP server serves $BRIDGE;
otherwise re-run with IPCONF='10.0.0.50/24,gw=10.0.0.1'."
say "  $ip"

step "Installing Docker"
# Debian renamed the compose package: bookworm ships docker-compose-v2, trixie
# ships docker-compose (which *is* v2). Asking for the wrong one fails with
# "Unable to locate package", which looks like a broken mirror.
in_ct "export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq >/dev/null
  apt-get install -y -qq docker.io git curl >/dev/null
  apt-get install -y -qq docker-compose-v2 >/dev/null 2>&1 \
    || apt-get install -y -qq docker-compose >/dev/null
  systemctl enable --now docker >/dev/null 2>&1 || true"
say "  $(in_ct 'docker --version') / $(in_ct 'docker compose version | head -1')"

step "Installing NetInv (this builds seven images — several minutes)"
in_ct "rm -rf /root/netinv && git clone -q --branch '$BRANCH' '$REPO' /root/netinv"
in_ct "cd /root/netinv && ./deploy/compose-app/quickstart.sh" | tail -5

pw=$(in_ct "grep '^NETINV_ADMIN_PASSWORD=' /root/netinv/deploy/compose-app/.env | cut -d= -f2-")

step "Verifying the things LXC changes"
verify || say "  (see doc 33 for what to do about the failures above)"

cat <<EOF

NetInv is up in CT $CTID.

  UI:        http://${ip}:8090
  Username:  admin
  Password:  ${pw}

  Re-check:  $0 verify $CTID
  Remove:    $0 destroy $CTID

Change the admin password after logging in. Retention defaults to two years;
Platform → Capacity reports whether this disk can hold that.
EOF
