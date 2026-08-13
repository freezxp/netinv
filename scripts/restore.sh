#!/usr/bin/env bash
# NetInv restore (NFR-24): restores a backup.sh output.
#
# DESTRUCTIVE against whatever it targets: it drops the database and replaces
# the metrics store. Targets are therefore explicit — by default it refuses to
# touch the production containers unless --force is given, because a restore
# script that can only aim at production cannot be rehearsed, and an unrehearsed
# restore is an assumption rather than a capability.
#
# Usage:
#   ./scripts/restore.sh <backup-dir>                      # into scratch containers
#   ./scripts/restore.sh <backup-dir> --force              # over the live stack
#   PG=other-pg VM=other-vm ./scripts/restore.sh <dir> --force
set -euo pipefail

SRC="${1:?usage: restore.sh <backup-dir> [--force]}"
FORCE="${2:-}"
PROD_PG="netinv-postgres-1"
PROD_VM="netinv-victoriametrics-1"
PG="${PG:-$PROD_PG}"
VM="${VM:-$PROD_VM}"
VM_PORT="${VM_PORT:-8428}"

[ -f "$SRC/netinv.pgdump" ] || { echo "no netinv.pgdump in $SRC"; exit 1; }

if [ "$PG" = "$PROD_PG" ] && [ "$FORCE" != "--force" ]; then
	cat >&2 <<EOF
Refusing to restore over the live stack without --force.

To rehearse the restore safely (this is the drill doc 20 §12 asks for), start
scratch containers and point this script at them:

  docker run -d --name drill-pg -e POSTGRES_USER=netinv \\
    -e POSTGRES_PASSWORD=drill -e POSTGRES_DB=postgres postgres:16-alpine
  docker run -d --name drill-vm -p 18428:8428 -v drill-vmdata:/storage \\
    victoriametrics/victoria-metrics:v1.103.0 \\
    -retentionPeriod=2y -storageDataPath=/storage

  PG=drill-pg VM=drill-vm VM_PORT=18428 ./scripts/restore.sh $SRC

  docker rm -f drill-pg drill-vm && docker volume rm drill-vmdata

The -v on drill-vm is required, not tidiness: this script unpacks the snapshot
through a helper container using --volumes-from, which inherits nothing if the
target has no volume at /storage, and the restore would go nowhere.

Verify against the live system at a timestamp inside the backup, not at "now" —
the restored store's newest sample is the moment the snapshot was taken, so an
instant query now legitimately returns fewer series and looks like a failure.

To restore over the live stack for real, pass --force.
EOF
	exit 2
fi

echo "== Restoring PostgreSQL into $PG (drop + recreate)"
docker exec "$PG" psql -U netinv -d postgres -q \
	-c "DROP DATABASE IF EXISTS netinv WITH (FORCE)" \
	-c "CREATE DATABASE netinv OWNER netinv"
docker exec -i "$PG" pg_restore -U netinv -d netinv --no-owner \
	< "$SRC/netinv.pgdump"

if [ -f "$SRC/vm-snapshot.tar" ]; then
	echo "== Restoring VictoriaMetrics into $VM from snapshot"
	# A VictoriaMetrics snapshot is a complete storage tree — metadata/,
	# indexdb/ and data/ side by side — not the contents of data/. Extracting
	# it into /storage/data nests it a level too deep: VM then starts, finds no
	# store it recognises, silently builds an empty one, and reports healthy
	# while every graph is blank. That is what this script did until the
	# 2026-08-13 drill caught it, so the layout is asserted below rather than
	# assumed.
	# Listed into a variable rather than piped into grep: with `set -o pipefail`,
	# `grep -q` exits at the first match, tar takes SIGPIPE, and the pipeline
	# reports failure for a snapshot that was perfectly good.
	entries=$(tar tf "$SRC/vm-snapshot.tar")
	case "$entries" in
	*"./indexdb/"*) ;;
	*)
		echo "snapshot does not look like a VM storage tree (no ./indexdb/)" >&2
		exit 1
		;;
	esac
	docker stop "$VM" -t 5 > /dev/null
	# No mkdir here on purpose. --volumes-from inherits nothing when the target
	# container has no volume mounted at /storage, and the previous version's
	# `mkdir -p` papered over exactly that: the snapshot was unpacked into a
	# throwaway container and discarded with it, leaving a healthy-looking VM
	# with no data. Failing on a missing /storage is the signal.
	docker run --rm --volumes-from "$VM" \
		-v "$(cd "$SRC" && pwd):/backup:ro" alpine \
		sh -c 'test -d /storage || { echo "no /storage from --volumes-from: does '"$VM"' mount a volume there?" >&2; exit 1; }
		       rm -rf /storage/data /storage/indexdb /storage/metadata /storage/cache
		       tar xf /backup/vm-snapshot.tar -C /storage'
	docker start "$VM" > /dev/null
fi

# Verify rather than announce. The failure this script had was silent, and a
# restore you have not checked is a backup you have not tested.
echo "== Verifying"
rows=$(docker exec "$PG" psql -U netinv -d netinv -tAc \
	"select count(*) from inventory.devices" 2>/dev/null || echo 0)
echo "  devices restored: $rows"
if [ -f "$SRC/vm-snapshot.tar" ]; then
	for _ in $(seq 1 30); do
		series=$(curl -sf "http://localhost:${VM_PORT}/api/v1/status/tsdb" 2>/dev/null |
			python3 -c "import json,sys;print(json.load(sys.stdin)['data'].get('totalSeries',0))" 2>/dev/null || echo 0)
		[ "${series:-0}" -gt 0 ] && break
		sleep 2
	done
	echo "  metric series restored: ${series:-0}"
	if [ "${series:-0}" -eq 0 ]; then
		echo "  FAILED: the metrics store came back empty — do not trust this restore" >&2
		exit 1
	fi
fi
echo "Restore complete. Restart NetInv services to pick up state."
