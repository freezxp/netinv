# Single-host deployment (Docker Compose)

The whole platform on one host: all six Go services + the frontend, running as
their built images against the compose data tier. This is the simplest real
deployment; for on-prem Kubernetes use the Helm charts and `docs/31-pilot-runbook.md`.

## Deploy

```bash
cd deploy/compose-app
cp .env.example .env
# Generate the two keys and set an admin password:
#   NETINV_MASTER_KEY / NETINV_JWT_SIGNING_KEY  →  openssl rand -base64 32
# Then, from the repo root:
cd ../..
docker compose up -d --wait postgres redis rabbitmq victoriametrics snmpsim mailhog
docker compose --env-file deploy/compose-app/.env \
  -f docker-compose.yml -f deploy/compose-app/docker-compose.deploy.yml \
  --profile app up -d --build
```

Open **https://localhost:8443** (8090 redirects there) and log in as `admin` with the
`NETINV_ADMIN_PASSWORD` from your `.env` (the admin is bootstrapped on first
boot against an empty database; change the password at first login).

## What runs

| URL / port | What |
|---|---|
| https://localhost:8443 | NetInv UI (nginx serving the SPA, proxying `/api` to the api service). http://localhost:8090 redirects here, or serves the app itself when `NETINV_FRONTEND_MODE=http` |
| :5672 / :15672 | RabbitMQ (+ management) |
| :8428 | VictoriaMetrics |
| :8025 | MailHog (email test inbox) |
| :1161/udp | SNMP simulator (community `public`) |

## Notes

- **Secrets**: `.env` is git-ignored. The master key encrypts the credential
  vault — losing it means re-entering SNMP credentials (doc 28 R-10).
- **TLS**: this single-host setup terminates TLS on 8443 with a self-signed
  certificate from `make-cert.sh`; 8090 redirects there and session cookies
  carry `Secure`. Replace `certs/netinv.{crt,key}` with a real pair for
  anything exposed. Setting `NETINV_FRONTEND_MODE=http` makes 8090 serve the
  app itself, for a TLS-terminating proxy in front that cannot trust the
  origin certificate — doc 20 §12.4 covers what that costs.
- **Poller state**: the buffer uses tmpfs here (single host, local broker). The
  k8s poller chart uses a PVC with `fsGroup` for persistence.
- **Managed data tier**: point `PG_PASSWORD`/`RABBIT_PASSWORD` (and the DSNs in
  the overlay) at your own PostgreSQL/RabbitMQ/VictoriaMetrics/Redis instead of
  the bundled dev containers for a production single-host install.

## Tear down

```bash
docker compose -f docker-compose.yml \
  -f deploy/compose-app/docker-compose.deploy.yml --profile app down
```
