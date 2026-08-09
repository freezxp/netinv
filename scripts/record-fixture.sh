#!/usr/bin/env bash
# Record an snmpwalk fixture from real hardware, with identity redacted.
#
# Doc 10 §6 asks every connector to ship recorded walks, because a fixture taken
# from a real device is the only thing that catches what a MIB document will
# not: an agent that reports speed in ifSpeed and leaves ifHighSpeed at zero, a
# vendor console answering with a net-snmp sysObjectID, a table that exists but
# is empty.
#
# It is also how this repository published a real device serial. A faithful
# recording carries the device's identity with it — serial, hostname, location,
# addresses, MACs — and fixtures are committed to a public repo. So redaction
# happens here, at the moment of recording, rather than being left to whoever
# remembers.
#
# Usage: scripts/record-fixture.sh <host> <community> <connector> [oid-root ...]
#
# Review the output before committing it. This redacts the fields that are known
# to carry identity; it cannot know that your sysDescr contains a site codename.
set -euo pipefail

host="${1:?usage: record-fixture.sh <host> <community> <connector> [oid-root ...]}"
community="${2:?missing community}"
connector="${3:?missing connector name}"
shift 3

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/connectors/$connector/testdata/$connector.snmpwalk"
mkdir -p "$(dirname "$out")"

roots=("$@")
if [ "${#roots[@]}" -eq 0 ]; then
	# System group plus the standard tables every connector builds on. Scoped
	# deliberately: a full .1.3.6.1 walk sweeps up ARP caches, address tables
	# and neighbour lists — other people's infrastructure, not this device's
	# behaviour.
	roots=(.1.3.6.1.2.1.1 .1.3.6.1.2.1.2.2.1 .1.3.6.1.2.1.31.1.1.1)
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

printf 'recording %s from %s\n' "$connector" "$host"
for r in "${roots[@]}"; do
	printf '  walking %s\n' "$r"
	before=$(wc -l <"$tmp")
	# snmpwalk exits 0 on "Timeout: No Response", so the exit status says
	# nothing; count lines instead. -r/-t give slow or rate-limiting agents
	# room — several devices here answer a single get instantly and then stall
	# partway through a walk.
	snmpwalk -v2c -c "$community" -r 3 -t 5 -On -Ot "$host" "$r" 2>/dev/null >>"$tmp" || true
	after=$(wc -l <"$tmp")
	printf '    %s rows\n' "$((after - before))"
done

if [ "$(wc -l <"$tmp")" -eq 0 ]; then
	printf '\nFAILED: the agent returned nothing.\n' >&2
	printf 'No fixture written. An empty file would satisfy connector-lint while\n' >&2
	printf 'testing nothing at all, which is worse than having no fixture.\n' >&2
	exit 1
fi

# Redaction runs in awk, splitting each line into OID and value so that only
# the value is touched. REDACT_OIDS carries vendor identity columns — a
# connector's own name/serial OIDs, which no generic rule can know about.
awk -v REDACT_OIDS="${REDACT_OIDS:-}" -v REDACT_INDEX_OIDS="${REDACT_INDEX_OIDS:-}" -f "$root/scripts/redact-walk.awk" "$tmp" >"$out"

cat >>"$out" <<EOF

# Recorded with scripts/record-fixture.sh and redacted at capture time.
# Serial numbers, MAC addresses, IP addresses, sysName, sysLocation and
# sysContact are replaced with documentation-range placeholders (RFC 5737,
# RFC 7042). The behaviour under test — which OIDs exist, which are absent,
# what types and ranges the agent returns — is preserved exactly.
EOF

printf 'wrote %s (%d lines)\n' "${out#"$root"/}" "$(wc -l <"$out")"
printf '\nReview it before committing. Redaction covers what is known to carry\n'
printf 'identity; it cannot know that a sysDescr names your organisation.\n'
