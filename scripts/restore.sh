#!/usr/bin/env bash
# NetInv restore (NFR-24): restores a backup.sh output into the compose data
# tier. DESTRUCTIVE — drops the current database and VM data.
# Usage: ./scripts/restore.sh <backup-dir>
set -euo pipefail

SRC="${1:?usage: restore.sh <backup-dir>}"
[ -f "$SRC/netinv.pgdump" ] || { echo "no netinv.pgdump in $SRC"; exit 1; }

echo "== Restoring PostgreSQL (drop + recreate)"
docker exec netinv-postgres-1 psql -U netinv -d postgres -q \
  -c "DROP DATABASE IF EXISTS netinv WITH (FORCE)" \
  -c "CREATE DATABASE netinv OWNER netinv"
docker exec -i netinv-postgres-1 pg_restore -U netinv -d netinv --no-owner \
  < "$SRC/netinv.pgdump"

if [ -f "$SRC/vm-snapshot.tar" ]; then
  echo "== Restoring VictoriaMetrics from snapshot"
  docker stop netinv-victoriametrics-1 -t 5 > /dev/null
  docker run --rm --volumes-from netinv-victoriametrics-1 \
    -v "$(cd "$SRC" && pwd):/backup:ro" alpine \
    sh -c "rm -rf /storage/data && mkdir -p /storage/data &&
           tar xf /backup/vm-snapshot.tar -C /storage/data"
  docker start netinv-victoriametrics-1 > /dev/null
fi
echo "Restore complete. Restart NetInv services to pick up state."
