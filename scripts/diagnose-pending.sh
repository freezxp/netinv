#!/usr/bin/env bash
# NetInv: why is a device stuck in 'pending'? (doc 07 §2, doc 11 §3)
#
# A device leaves 'pending' in exactly one place — the sync apply transaction
# in backend/internal/inventory/adapters/postgres/sync.go, which sets
# status='active' when a good snapshot lands. Nothing else promotes a device:
# ICMP, traffic and health polls can all succeed while it sits in 'pending'
# forever. So 'pending' always means "no sync result has ever been applied",
# and there are four distinct reasons for that:
#
#   A. no sync schedule    — the device's profile omits 'sync' from
#                            families_enabled, so insertSchedules deleted the
#                            row. Nothing is ever dispatched. Silent.
#   B. never dispatched    — scheduled, but no poller consumes the device's
#                            site queue, so jobs just accumulate. Also silent.
#   C. collection failed   — the poller ran it and SNMP failed. The reason is
#                            in platform.sync_runs.error and NOWHERE else: no
#                            API or UI surfaces it.
#   D. apply failed        — collection succeeded but the write is erroring
#                            and requeuing in the api.
#
# Read-only: this script only SELECTs, lists queues and greps logs.
#
# Usage:
#   ./scripts/diagnose-pending.sh                 # every pending device
#   ./scripts/diagnose-pending.sh juniper-junos   # one connector only
#   PG=my-pg RABBIT=my-rabbit ./scripts/diagnose-pending.sh
set -euo pipefail

CONNECTOR="${1:-}"
LOG_SINCE="${LOG_SINCE:-24h}"

# Container names differ with the compose project name (it defaults to the
# directory the stack was brought up from), so guess from `docker ps` and let
# the operator override. Guessing beats hard-coding: the pilot's project name
# and a quickstart.sh deployment's are not the same.
guess() {
	docker ps --format '{{.Names}}' | grep -m1 -E -- "-$1-[0-9]+$" ||
		docker ps --format '{{.Names}}' | grep -m1 -- "$1" || true
}
PG="${PG:-$(guess postgres)}"
RABBIT="${RABBIT:-$(guess rabbitmq)}"
API="${API:-$(guess api)}"
POLLER="${POLLER:-$(guess poller)}"

[ -n "$PG" ] || { echo "no postgres container found — set PG=<name>" >&2; exit 1; }

PG_USER="${PG_USER:-netinv}"
PG_DB="${PG_DB:-netinv}"

section() { printf '\n\033[1m── %s ─────────────────────────────────\033[0m\n' "$1"; }

# psql wrappers: q() for human-readable output, q1() for parseable rows.
q() { docker exec -i "$PG" psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 "$@"; }
q1() { q -t -A -F'|' "$@"; }

# The connector id is interpolated into SQL, so hold it to the shape a
# connector id actually has (sdk.Info.ID) rather than trusting the caller.
case "$CONNECTOR" in
*[!a-z0-9-]*) echo "connector id must be [a-z0-9-]: $CONNECTOR" >&2; exit 1 ;;
esac
# Empty CONNECTOR means "any connector": '' IN ('', ...) is true for every row.
filter="'$CONNECTOR' IN ('', d.connector_id)"

echo "postgres=$PG rabbitmq=${RABBIT:-<none>} api=${API:-<none>} poller=${POLLER:-<none>}"
if [ -n "$CONNECTOR" ]; then
	echo "connector filter: $CONNECTOR"
fi

# ---------------------------------------------------------------- 1. devices
section "1. Pending devices — credential, profile and schedules"
q -x <<SQL
SELECT d.name, d.id AS device_id, host(d.mgmt_ip) AS ip, d.status,
       d.site_id, d.connector_id, d.created_at,
       coalesce(d.attrs->>'snmp_port','161') AS snmp_port,
       c.name AS credential,
       c.meta->>'version'        AS snmp_version,
       c.meta->>'auth_protocol'  AS auth_proto,
       c.meta->>'priv_protocol'  AS priv_proto,
       p.name AS profile, p.families_enabled,
       p.snmp_timeout_ms, p.snmp_retries, p.sync_interval_s,
       (SELECT string_agg(ps.family || '=' || ps.enabled || '@' ||
                          coalesce(ps.last_run_at::text, 'never'), '  ')
          FROM platform.polling_schedule ps WHERE ps.device_id = d.id) AS schedules
FROM inventory.devices d
JOIN inventory.credentials c ON c.id = d.credential_id
JOIN platform.polling_profiles p ON p.id = d.profile_id
WHERE d.status = 'pending' AND $filter
ORDER BY d.created_at;
SQL

# Nothing pending: say so and stop rather than printing four empty sections.
pending_count=$(q1 -c "SELECT count(*) FROM inventory.devices d
                       WHERE d.status = 'pending' AND $filter;")
if [ "$pending_count" = "0" ]; then
	echo "No pending devices. Nothing to diagnose."
	exit 0
fi

# ------------------------------------------------------------- 2. sync runs
section "2. Sync runs — the error text lives here and nowhere else"
q -x <<SQL
SELECT d.name, r.started_at, r.trigger, r.status, r.changes_count, r.error
FROM platform.sync_runs r
JOIN inventory.devices d ON d.id = r.device_id
WHERE d.status = 'pending' AND $filter
ORDER BY r.started_at DESC
LIMIT 20;
SQL

# ---------------------------------------------------------------- 3. queues
section "3. Site queues — a site with no consumer is never polled"
if [ -n "$RABBIT" ]; then
	# Only the queues that matter: one per site holding a pending device.
	sites=$(q1 -c "SELECT DISTINCT 'poll.site.' || d.site_id
	               FROM inventory.devices d
	               WHERE d.status = 'pending' AND $filter;")
	queues=$(docker exec "$RABBIT" rabbitmqctl -q list_queues \
		name messages messages_unacknowledged consumers 2>/dev/null || true)
	if [ -z "$queues" ]; then
		echo "could not list queues on $RABBIT"
	else
		printf 'queue\tmessages\tunacked\tconsumers\n'
		while read -r site_queue; do
			[ -n "$site_queue" ] || continue
			printf '%s\n' "$queues" | awk -v q="$site_queue" '$1 == q { print; found=1 }
				END { if (!found) print q "\t(queue does not exist)" }'
		done <<<"$sites"
		echo
		echo "sync.results (consumed by the api):"
		printf '%s\n' "$queues" | grep -E '^sync\.results' || echo "  sync.results missing"
	fi
else
	echo "no rabbitmq container found — set RABBIT=<name> to check consumers"
fi

# ------------------------------------------------------------------ 4. logs
section "4. Logs (last $LOG_SINCE)"
# Both of the interesting failures repeat on a timer — the poller redials a
# dead broker every 3 s, and a failing sync apply requeues every second — so a
# raw tail is all storm and no signal. Count the distinct messages instead,
# and show raw lines only for the pending devices themselves.
ids=$(q1 -c "SELECT coalesce(string_agg(d.id, '|'), 'no-such-device')
             FROM inventory.devices d WHERE d.status = 'pending' AND $filter;")

log_msgs() { # container, grep pattern — counted distinct messages, newest first
	docker logs --since "$LOG_SINCE" "$1" 2>&1 | grep -E "$2" |
		sed 's/.*"msg":"\([^"]*\)".*/\1/' | sort | uniq -c | sort -rn | head -10 ||
		echo "  (nothing)"
}

if [ -n "$POLLER" ]; then
	echo "--- $POLLER: message counts"
	log_msgs "$POLLER" 'poll failed|job stream|malformed'
	echo "--- $POLLER: failures for these devices"
	docker logs --since "$LOG_SINCE" "$POLLER" 2>&1 |
		grep -E 'poll failed' | grep -E "$ids" | tail -10 || echo "  (nothing)"
fi
if [ -n "$API" ]; then
	echo "--- $API: message counts (a repeating requeue is class D)"
	log_msgs "$API" 'sync result|requeued|duplicate key'
	echo "--- $API: failures for these devices"
	docker logs --since "$LOG_SINCE" "$API" 2>&1 |
		grep -Ei 'sync result|requeued|duplicate key' | grep -E "$ids" |
		tail -10 || echo "  (nothing)"
fi

# --------------------------------------------------------------- 5. verdict
section "5. Verdict per device"
# One row per pending device carrying just enough to classify it: whether a
# sync schedule exists, whether it has ever run, and the newest sync_run.
verdicts=$(q1 <<SQL
SELECT d.name, host(d.mgmt_ip), d.site_id,
       coalesce((SELECT ps.enabled::text FROM platform.polling_schedule ps
                  WHERE ps.device_id = d.id AND ps.family = 'sync'), 'none'),
       coalesce((SELECT ps.last_run_at::text FROM platform.polling_schedule ps
                  WHERE ps.device_id = d.id AND ps.family = 'sync'), ''),
       coalesce((SELECT r.status::text FROM platform.sync_runs r
                  WHERE r.device_id = d.id ORDER BY r.started_at DESC LIMIT 1), ''),
       coalesce((SELECT replace(coalesce(r.error,''), E'\n', ' ')
                   FROM platform.sync_runs r
                  WHERE r.device_id = d.id ORDER BY r.started_at DESC LIMIT 1), '')
FROM inventory.devices d
WHERE d.status = 'pending' AND $filter
ORDER BY d.name;
SQL
)
while IFS='|' read -r name ip site sched last_run run_status run_err; do
	[ -n "$name" ] || continue
	printf '\n%s (%s)\n' "$name" "$ip"
	case "$sched" in
	none)
		echo "  A. NO SYNC SCHEDULE. The device's polling profile omits 'sync'"
		echo "     from families_enabled, so insertSchedules dropped the row and"
		echo "     nothing is ever dispatched. Add 'sync' to the profile, then"
		echo "     re-save the device to rewrite its schedules."
		continue
		;;
	false)
		echo "  A. SYNC SCHEDULE DISABLED (enabled=false). Re-enable it."
		continue
		;;
	esac
	if [ -z "$last_run" ] && [ -z "$run_status" ]; then
		echo "  B. NEVER DISPATCHED. Scheduled but no job has run. Check that"
		echo "     poll.site.$site has a consumer in section 3 — a device whose"
		echo "     site no poller serves is silently never polled. Also confirm"
		echo "     the scheduler container is running."
		continue
	fi
	case "$run_status" in
	failed)
		echo "  C. COLLECTION FAILED. SNMP did not answer the poller:"
		echo "     ${run_err:-(no error recorded)}"
		echo "     Reproduce from the poller's own network position with step 6."
		;;
	ok)
		echo "  D. SYNC SUCCEEDED BUT THE DEVICE IS STILL PENDING. The apply is"
		echo "     erroring and requeuing — see the api log in section 4 for"
		echo "     'sync result requeued' or a duplicate-key error."
		;;
	'')
		echo "  B/C. Dispatched at $last_run but no sync_run was recorded, so the"
		echo "     poller never published a result. Check the poller log above."
		;;
	*)
		echo "  Newest sync run is '$run_status' — still in flight, re-run shortly."
		;;
	esac
done <<<"$verdicts"

# ------------------------------------------------------- 6. reproduce / fix
section "6. Reproduce from the poller, and force a re-sync"
cat <<'EOF'
The poller image is distroless, so there is no shell to exec into. Attach a
sidecar to its network namespace instead — same source address, same routes,
which is what an ACL or a Junos routing-instance restriction keys on. A walk
that works from your laptop and fails here is exactly that difference:

  docker run --rm --network container:POLLER alpine sh -c \
    'apk add -q net-snmp-tools && \
     snmpget -v2c -c COMMUNITY -t 5 -r 2 DEVICE_IP 1.3.6.1.2.1.1.5.0 && \
     time snmpbulkwalk -v2c -c COMMUNITY -t 5 -r 2 -Cr25 DEVICE_IP 1.3.6.1.2.1.2.2 | wc -l'

For SNMPv3, match the stored credential from section 1 exactly (-a SHA is
SHA-1, not SHA-256):

  snmpget -v3 -l authPriv -u USER -a SHA -A 'AUTHPASS' -x AES -X 'PRIVPASS' \
    DEVICE_IP 1.3.6.1.2.1.1.5.0

Time the walk. CollectInventory walks ifTable AND ifXTable inside a 60 s job
budget (pollerrt/runtime.go), so a walk that is merely slow by hand is a hard
failure in the poller. Raise snmp_timeout_ms / snmp_retries on the profile, or
cut the interface count at the device.

Then force a sync rather than waiting out sync_interval_s (6 h by default) —
"Sync now" on the device page, or:

  curl -sk -X POST -b cookies.txt https://HOST:8443/api/v1/devices/DEVICE_ID/sync

and re-run this script a minute later.
EOF
