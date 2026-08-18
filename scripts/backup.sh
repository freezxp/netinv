#!/usr/bin/env bash
# NetInv backup (NFR-31): PostgreSQL logical dump + VictoriaMetrics snapshot.
# Compose-targeted; the k8s variant swaps docker exec for kubectl exec and the
# snapshot copy for object storage (doc 19 runbook).
# Usage: ./scripts/backup.sh [output-dir]
#        KEEP=7 ./scripts/backup.sh          # also prune to the newest 7
#
# Retention is opt-in: unset KEEP keeps every backup, which is the behaviour a
# cron entry written against this script already depends on. It is worth
# setting, though — nothing here has ever removed anything, so a scheduled
# backup grows without bound until the filesystem is full, and on one host the
# first thing that broke was the upgrade that takes these backups.
set -euo pipefail

OUT="${1:-./backups}/$(date -u +%Y%m%d-%H%M%S)"
mkdir -p "$OUT"

echo "== PostgreSQL dump"
docker exec netinv-postgres-1 pg_dump -U netinv -d netinv -Fc \
  > "$OUT/netinv.pgdump"

echo "== VictoriaMetrics snapshot"
SNAP=$(curl -sf http://localhost:8428/snapshot/create |
  python3 -c "import json,sys;print(json.load(sys.stdin)['snapshot'])")
# Snapshots are hardlink/symlink trees into /storage — tar with -h to
# dereference, or the copy breaks outside the container.
docker exec netinv-victoriametrics-1 tar chf - -C "/storage/snapshots/$SNAP" . \
  > "$OUT/vm-snapshot.tar"
curl -sf "http://localhost:8428/snapshot/delete?snapshot=$SNAP" > /dev/null

du -sh "$OUT"/* | sed 's/^/  /'
echo "Backup complete: $OUT"

# Only directories named like the timestamps this script generates are ever
# removed, so a KEEP set against a directory holding anything else cannot lose
# it.
if [ -n "${KEEP:-}" ] && [ "$KEEP" -gt 0 ]; then
	stale=$(find "$(dirname "$OUT")" -mindepth 1 -maxdepth 1 -type d \
		-regextype posix-extended -regex '.*/[0-9]{8}-[0-9]{6}$' |
		sort -r | tail -n +$((KEEP + 1)))
	if [ -n "$stale" ]; then
		echo "Pruning $(printf '%s\n' "$stale" | wc -l) older backup(s), keeping $KEEP"
		printf '%s\n' "$stale" | while IFS= read -r d; do
			[ -n "$d" ] || continue
			rm -rf -- "$d"
		done
	fi
fi
