#!/usr/bin/env bash
# One-command NetInv test deployment on a single host with Docker.
# Generates secrets on first run, builds images, starts the whole stack, and
# prints the URL + admin login. Safe to re-run (idempotent).
#
#   ./deploy/compose-app/quickstart.sh          # up
#   ./deploy/compose-app/quickstart.sh down     # stop + remove
#   ./deploy/compose-app/quickstart.sh reset    # stop + wipe all data
set -euo pipefail

# Resolve repo root regardless of where this is invoked from.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ENV_FILE="$SCRIPT_DIR/.env"
COMPOSE=(docker compose --env-file "$ENV_FILE"
  -f "$ROOT/docker-compose.yml"
  -f "$SCRIPT_DIR/docker-compose.deploy.yml")

cmd="${1:-up}"

case "$cmd" in
  down)
    "${COMPOSE[@]}" --profile app down
    exit 0 ;;
  reset)
    "${COMPOSE[@]}" --profile app down -v
    echo "Stack stopped and all data volumes removed."
    exit 0 ;;
  up) ;;
  *) echo "usage: quickstart.sh [up|down|reset]"; exit 1 ;;
esac

command -v docker >/dev/null || { echo "Docker is required."; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required."; exit 1; }

# 1. Secrets — generate once, keep thereafter.
if [ ! -f "$ENV_FILE" ]; then
  echo "==> Generating secrets in $ENV_FILE"
  ADMIN_PW="NetInv-$(openssl rand -hex 5)"
  cat > "$ENV_FILE" <<EOF
PG_PASSWORD=$(openssl rand -hex 12)
RABBIT_PASSWORD=$(openssl rand -hex 12)
NETINV_MASTER_KEY=$(openssl rand -base64 32)
NETINV_JWT_SIGNING_KEY=$(openssl rand -base64 32)
NETINV_ADMIN_PASSWORD=$ADMIN_PW
NETINV_UI_URL=http://localhost:8090
NETINV_INSECURE_COOKIES=1
EOF
  chmod 600 "$ENV_FILE"
else
  echo "==> Using existing secrets ($ENV_FILE)"
fi
ADMIN_PW="$(grep '^NETINV_ADMIN_PASSWORD=' "$ENV_FILE" | cut -d= -f2)"

# 2. Data tier (bundled instances). Uses the same env file so the postgres /
# rabbitmq passwords match the app-side DSNs.
echo "==> Starting data tier (postgres, redis, rabbitmq, victoriametrics, snmpsim, mailhog)"
"${COMPOSE[@]}" up -d --wait \
  postgres redis rabbitmq victoriametrics snmpsim mailhog

# 3. Application (six services + frontend), built from source.
echo "==> Building and starting NetInv services"
"${COMPOSE[@]}" --profile app up -d --build

# 4. Wait for the api to be ready through the frontend proxy.
echo -n "==> Waiting for the UI to come up"
for _ in $(seq 1 60); do
  if curl -sf -o /dev/null "http://localhost:8090/"; then break; fi
  echo -n "."; sleep 2
done
echo

cat <<EOF

  NetInv is up.

    UI:        http://localhost:8090
    Username:  admin
    Password:  $ADMIN_PW

    RabbitMQ:  http://localhost:15672   VictoriaMetrics: http://localhost:8428
    MailHog:   http://localhost:8025    SNMP sim community: public

  Stop:  ./deploy/compose-app/quickstart.sh down
  Wipe:  ./deploy/compose-app/quickstart.sh reset

  Next: log in, change the admin password, then Platform → Sites/Credentials
  and Inventory → Add device. See docs/32-quickstart.md.
EOF
