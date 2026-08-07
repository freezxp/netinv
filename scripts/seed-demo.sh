#!/usr/bin/env bash
# Seed a demo fleet against a running netinv-api (NFR-73). All devices point
# at the snmpsim container (loopback aliases 127.0.0.x share the simulator).
# Usage: NETINV_ADMIN_PASSWORD=... ./scripts/seed-demo.sh [api-url]
set -euo pipefail

API="${1:-http://localhost:8080}"
PASS="${NETINV_ADMIN_PASSWORD:?set NETINV_ADMIN_PASSWORD}"

token() {
  curl -sf -X POST "$API/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" |
    python3 -c "import json,sys;print(json.load(sys.stdin)['access_token'])"
}
T=$(token)
auth=(-H "Authorization: Bearer $T" -H 'Content-Type: application/json')

get_or_create_site() { # name location
  local id
  id=$(curl -sf "$API/api/v1/sites" "${auth[@]}" |
    python3 -c "import json,sys;[print(s['id']) for s in json.load(sys.stdin)['data'] if s['name']=='$1']")
  if [ -z "$id" ]; then
    id=$(curl -sf -X POST "$API/api/v1/sites" "${auth[@]}" \
      -d "{\"name\":\"$1\",\"location\":\"$2\"}" |
      python3 -c "import json,sys;print(json.load(sys.stdin)['id'])")
  fi
  echo "$id"
}

CRED=$(curl -sf "$API/api/v1/credentials" "${auth[@]}" |
  python3 -c "import json,sys;[print(c['id']) for c in json.load(sys.stdin)['data'] if c['name']=='lab-v2c']" | head -1)
if [ -z "$CRED" ]; then
  CRED=$(curl -sf -X POST "$API/api/v1/credentials" "${auth[@]}" \
    -d '{"name":"lab-v2c","kind":"snmp_v2c","secret":{"community":"public"}}' |
    python3 -c "import json,sys;print(json.load(sys.stdin)['id'])")
fi

EAST=$(get_or_create_site "DC East" "East hall")
WEST=$(get_or_create_site "DC West" "West hall")

i=0
for name in edge-rtr-1 edge-rtr-2 agg-sw-1 agg-sw-2 access-sw-1 \
            access-sw-2 access-sw-3 core-fw-1 lab-sw-1 lab-sw-2; do
  i=$((i + 1))
  site=$EAST
  [ $((i % 2)) -eq 0 ] && site=$WEST
  ip="127.0.1.$i"
  curl -s -X POST "$API/api/v1/devices" "${auth[@]}" -d "{
      \"name\": \"$name\", \"mgmt_ip\": \"$ip\", \"site_id\": \"$site\",
      \"credential_id\": \"$CRED\", \"snmp_port\": 1161,
      \"tags\": [\"demo\"]}" |
    python3 -c "import json,sys
b=json.load(sys.stdin)
print(' created' if 'id' in b else ' skipped', '$name:', b.get('id', b.get('error',{}).get('message','')))"
done
echo "Demo fleet seeded. Pollers for both sites must be running to collect."
