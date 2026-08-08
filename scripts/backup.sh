#!/usr/bin/env bash
# NetInv backup (NFR-31): PostgreSQL logical dump + VictoriaMetrics snapshot.
# Compose-targeted; the k8s variant swaps docker exec for kubectl exec and the
# snapshot copy for object storage (doc 19 runbook).
# Usage: ./scripts/backup.sh [output-dir]
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
