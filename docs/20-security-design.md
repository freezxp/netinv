# 20 — Security Design

**Status:** draft · **Depends on:** 05, 08, ADR-010/011 · NFR targets: 04 §5

## 1. Threat model (STRIDE summary — assets: device credentials, network topology data, platform access)

| Threat | Vector | Primary controls |
|---|---|---|
| Credential theft | DB dump, API leak, log leak | envelope encryption (§7), write-only API (§6), redaction (§10), no-secret invariant tests (doc 24) |
| Spoofed poller | rogue agent joins, feeds false data / drains jobs | enrollment tokens + approval (§8), per-site AMQP users/vhosts, TLS |
| API abuse | stolen token, brute force | short JWT TTL, refresh rotation + reuse detection (doc 07 §4), lockout, rate limits (§9) |
| Privilege escalation | RBAC gaps | deny-by-default permission middleware (§5), authz tests per endpoint |
| Topology disclosure | map/inventory read by wrong party | RBAC floors on maps, TLS everywhere, audit trail |
| Tampered supply chain | malicious image/dependency | doc 25: pinned digests, SBOM, scan gates, signed images |
| SNMP exposure | v2c community sniffing, device ACL gaps | prefer v3 authPriv (UI nudges: v2c flagged "legacy"), device-side ACL guidance (doc 18), never SNMP write (PRD non-goal) |

## 2. AuthN (ADR-010)

Local accounts v1. Argon2id (m=64MiB, t=3, p=2 — tuned at deploy), per-user salt, PHC-format storage. Access JWT: **Ed25519-signed**, 15 min TTL, claims `sub`, `preferred_username`, `roles[]`, `tenant`, `jti`, `iss=netinv`, `aud=netinv-api` — deliberately OIDC-shaped; Keycloak later = new `iss`/JWKS config in `TokenVerifier` (doc 17 §5). Refresh: opaque 256-bit, SHA-256-stored, rotation families with reuse detection → family revocation (doc 07 §4). API tokens: `nv_`-prefixed opaque, scoped subset of owner's permissions, hashed at rest. Logout/deactivation → refresh revocation + access tokens die ≤15 min (jti denylist in Redis closes the gap to ≤60 s for deactivation, FR-AUTH-07).

## 3. Session & transport

TLS 1.2+ everywhere external (NFR-40): HTTPS ingress (HSTS, TLS terminated at nginx), AMQPS 5671 for pollers (server cert from corporate PKI/cert-manager; poller pins CA), SMTP STARTTLS. In-cluster plaintext accepted v1 behind NetworkPolicies; service-mesh mTLS is a roadmap ADR. Cookies: refresh token httpOnly+Secure+SameSite=Strict, path-scoped `/api/v1/auth`. CORS: same-origin default (SPA served from same host); configurable allowlist for API clients. CSP, X-Content-Type-Options, frame-ancestors none.

## 4. Password & account policy

Min 12 chars, zxcvbn score ≥3, no composition rules (NIST 800-63B), breach-list check offline (top-100k list embedded). Forced change on first login (seeded admin). Lockout per FR-AUTH-04. MFA (TOTP) is P1 — schema reserves `users.mfa jsonb`.

## 5. RBAC permission matrix (FR-RBAC-01)

Permissions are `resource:action`; roles are permission sets (doc 08 `roles.permissions`). Middleware: every route declares its permission (doc 09); no declaration = request refused (fail-closed, tested).

| Permission | Admin | Operator | Read-Only | Auditor |
|---|---|---|---|---|
| devices:read, metrics:read, maps:read, alerts:read, platform:read | ✔ | ✔ | ✔ | ✔ |
| alerts:ack, silences | ✔ | ✔ | — | — |
| maps:write | ✔ | ✔ | — | — |
| devices:write (onboard/edit/sync/import) | ✔ | ✔ | — | — |
| devices:admin (hard purge) | ✔ | — | — | — |
| alerts:admin (rules) | ✔ | ✔ | — | — |
| credentials:read/write | ✔ | — | — | — |
| platform:write (sites, pollers, discovery rules) | ✔ | — | — | — |
| users:read/write, settings:write | ✔ | — | — | — |
| audit:read | ✔ | — | — | ✔ |
| exports (inventory) | ✔ | ✔ | ✔ | ✔ (audit exports too) |

Auditor is the "compliance eyes": sees everything including audit, mutates nothing, cannot ack.

## 6. Secret-material handling rules

Secrets (SNMP credentials, SMTP/webhook/Slack secrets, tokens) are: write-only through the API (create/rotate only; GET returns metadata); encrypted at rest (§7); decrypted **only** where used (poller job execution path, notifier send path); never logged, never in events, never in error strings, never in metric labels (redaction layer + invariant tests, NFR-41); transmitted to pollers per-job over AMQPS (poller holds them in memory only — a stolen poller disk yields no secrets; buffer stores samples, not credentials).

## 7. Envelope encryption (ADR-011)

Per secret: random 256-bit DEK → AES-256-GCM(secret JSON, random 96-bit nonce) → DEK wrapped by master KEK (also AES-256-GCM with its own random nonce — authenticated wrapping without a separate KWP implementation) → store ciphertext + wrapped DEK + `key_version`. KEK from k8s Secret (env), 32 bytes. **Rotation:** new KEK as version n+1 → background job re-wraps DEKs (cheap; no payload re-encryption) → old KEK retired after full re-wrap; `key_version` makes this resumable. `KeyProvider` interface (doc 17 §5) is the Vault/KMS seam. KEK custody runbook: generation (`openssl rand`), storage in team password manager + k8s Secret, break-glass recovery, annual rotation.

## 8. Poller & connector authentication ("secure connector authentication")

Poller enrollment: admin issues one-time token (15 min TTL) → poller presents it on `POST /pollers/register` over TLS → API returns per-poller AMQP credentials (unique user, site-scoped vhost permissions: consume `poll.site.<its-site>`, publish `metrics.ingest` only) → admin approves before jobs flow (FR-PLT-02). Compromised site ⇒ blast radius = that site's queue. Heartbeat gap >2 min → alert + optional auto-disable. Credential rotation per poller via re-enrollment. Connectors (in-process v1) inherit poller trust; the future gRPC plugin seam adds per-plugin handshake tokens (ADR-014).

## 9. Rate limiting & abuse controls (NFR-44)

Redis sliding window: login 10/min/IP + lockout; API 600 req/min/token burst 100; exports 10/h/user; discovery sweeps concurrency-capped (doc 11 §7). 429 + `Retry-After`. Per-token limits configurable post-v1.

## 10. Audit & redaction

Audit coverage per FR-AUD-01 with before/after diffs; diff serializer strips fields tagged `secret` (allowlist approach: only schema-known fields serialize). Log redaction middleware masks anything matching secret field names as defense-in-depth (doc 21). Audit store append-only at the DB-grant level (doc 08).

## 11. OAuth2 positioning

The brief lists OAuth2: v1 implements the resource-server side (Bearer tokens, OIDC-shaped claims). Full OAuth2/OIDC flows (auth code + PKCE) arrive with Keycloak (doc 29) — the SPA then swaps its login form for a redirect; API changes = config only (ADR-010). No half-built local OAuth server in v1: fewer moving parts, smaller attack surface.

## 12. Release security checklist (gates v1.0, PRD §6)

### 12.1 TLS

The single-host deployment terminates TLS at nginx (`deploy/compose-app/frontend-tls.conf.template`). Port 80 exists only to issue a 308 to HTTPS and to serve an ACME challenge path; it is not a second way in.

- **TLS 1.2 and 1.3 only**, forward-secrecy ciphers only — every offered suite is ECDHE, so a stolen key cannot decrypt captured traffic. Session tickets off.
- **Headers:** HSTS, CSP, `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Cross-Origin-Opener-Policy`, `Permissions-Policy`. The CSP is `script-src 'self'` with no `unsafe-inline` — the app is self-contained, so `connect-src 'self'` leaves injected script nowhere to send anything. `style-src` needs `unsafe-inline` because the charting libraries set element styles directly.
- **A trap worth knowing:** `add_header` in a `location` block *replaces* the inherited set rather than adding to it. The headers are therefore repeated in each location, or every static asset silently loses them.
- **Certificates:** `deploy/compose-app/make-cert.sh` generates a self-signed pair with correct SANs if none exists, and never overwrites one. Replace `certs/netinv.{crt,key}` with a real pair and restart the frontend. The `certs/` directory is gitignored — this repository is public (ADR-019).
- **`NETINV_INSECURE_COOKIES` now defaults to 0**, so session cookies carry `Secure`. Set it to 1 only for a deliberately plain-HTTP deployment: with Secure cookies over HTTP the browser accepts the login response and never sends the cookie back, which presents as a broken login rather than a configuration choice.

### 12.2 Results, 2026-08-13

Run against the pilot over TLS. This closes the *TLS config scan* line below; the soak, staging-Kubernetes and restore-drill items remain open (ADR-023).

| Check | Tool | Result |
|---|---|---|
| Go dependencies | `govulncheck` | 0 called vulnerabilities, both modules. One advisory in a *required but never imported* module (`x/crypto/openpgp`, no fix published) — confirmed absent from the dependency graph |
| Frontend dependencies | `npm audit` | **3 advisories fixed** — `react-router` open redirect → XSS (CVSS 6.9) and SSR constructor injection. Upgraded 6.30.4 → 7.18.2; there is no patched 6.x |
| Container images | `trivy` HIGH+CRITICAL | **35 → 0.** The `nginx:1.27-alpine` base carried 2 CRITICAL OpenSSL CVEs, which matters directly because that container terminates TLS. Bumped to 1.29-alpine plus `apk upgrade` at build. Backend images were already 0 |
| Secrets in repo | `gitleaks` | No leaks across 115 commits |
| TLS configuration | `testssl.sh` | TLS 1.0/1.1/SSLv2/SSLv3 not offered; not vulnerable to Heartbleed, CCS, Ticketbleed, ROBOT, CRIME, BREACH, POODLE, SWEET32, FREAK, DROWN, LOGJAM, BEAST, LUCKY13, Winshock; no RC4. Certificate is self-signed, so chain-of-trust and OCSP are expected failures until a real one is installed |
| Authentication | manual | Every protected endpoint 401s unauthenticated. Forged bearer and `alg=none` JWT both rejected |
| Authorization | manual, `readonly` account | Reads 200; `users`, `credentials`, `audit-events` and all writes 403; privilege escalation (creating an admin) refused |
| Secret leakage | manual | No secret-shaped field in any admin-readable response — credentials return id/name/kind/count only |
| SQL injection | manual | Union, tautology, stacked-statement and `pg_sleep` time-based payloads: no delay, no row-count change, tables intact — parameterised throughout |
| Brute force | manual | 5 failures lock the account for 15 minutes (423), including for the correct password. Deactivated accounts are refused |

Two items in the table below remain unverified: **backup restore drill** and the **authz test suite covering every endpoint × every role** — the manual probe above covers `readonly` against the main surfaces, not the full matrix.



Dependency + container scan clean (critical/high) · secrets-in-repo scan (gitleaks) clean · authz test suite: every endpoint × every role matches §5 · no-secret-leak invariant tests green · TLS config scan (testssl) on staging · seeded-admin flow forces password change · backup restore drill performed · threat-model review of any scope added since this doc.
