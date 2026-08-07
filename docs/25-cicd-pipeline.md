# 25 — CI/CD Pipeline (GitHub Actions → GHCR → on-prem k8s)

**Status:** draft · **Depends on:** 24, 19, ADR-016

## 1. Principles

Trunk-based: short-lived branches → PR → squash to `main`; `main` is always releasable. Every merge produces deployable images; **deploys are pulled by the cluster** (no GitHub→on-prem credentials — doc 18 firewall matrix). Releases are tags (`v1.4.0`, SemVer). Path-filtered jobs keep the monorepo fast (docs-only PRs run docs checks only).

## 2. Workflows

### `ci.yml` — every PR + push to main
```
changes (paths-filter) ─┬─ backend:  go build ▸ golangci-lint ▸ go-arch-lint (boundary
                        │            rules, docs 12/13) ▸ unit ▸ integration
                        │            (testcontainers: PG/RMQ/VM/Redis) ▸ coverage gates
                        │            ▸ openapi generate+drift check (doc 24 §contract)
                        ├─ connectors: connector-lint (sdk-only imports, fixtures
                        │            present) ▸ tests ▸ zero-core-diff check (NFR-72)
                        ├─ frontend: pnpm install ▸ typecheck (incl. generated API
                        │            types) ▸ eslint (+boundary rules) ▸ vitest
                        │            ▸ build ▸ bundle-size budget (doc 14)
                        ├─ security: gitleaks ▸ govulncheck ▸ npm audit (fail: high+)
                        ├─ helm:     helm lint ▸ kubeconform ▸ chart-testing
                        └─ docs:     lychee link check ▸ mermaid compile check ▸
                                     "docs updated?" PR-template reminder (NFR-70)
```
Target: PR feedback <10 min (unit/lint fast-fail before integration).

### `e2e.yml` — merge to main + nightly
Compose the full stack (all services + snmpsim + MailHog) from freshly built images → Playwright journeys (doc 24 §3) → artifacts: traces/screenshots on failure.

### `build.yml` — merge to main + tags
Multi-arch (amd64/arm64) distroless images per service + frontend-nginx, buildx cache; tags: `sha-<short>`, `main`, and SemVer on release tags. Push GHCR → **trivy scan (gate: critical)** → SBOM (syft, attached) → **cosign sign** (keyless OIDC). Helm charts packaged and pushed as OCI to GHCR with matching version.

### `release.yml` — on tag `v*`
Verify `main` E2E green → generate release notes from conventional commits → GitHub Release with chart/SBOM links → bump `deploy/environments/staging` image digests (GitOps commit).

### `nightly.yml`
Load profile on staging (doc 24 §4) → SLO assertions (fail = issue auto-filed) · staging **backup-restore drill** (restore last night's PG backup + VM snapshot into a scratch namespace, run smoke) — NFR-31's "backups are tested, not hoped".

## 3. CD — on-prem pull model

**Argo CD** (or Flux; chart-agnostic) in the on-prem cluster watches `deploy/environments/{staging,prod}` in this repo (image digests + values per env):
- **staging:** auto-sync on every main merge → smoke suite runs in-cluster (Argo hook Job) → notifies.
- **prod:** sync gated on git tag promotion PR (human approves the environment diff) → Helm upgrade with pre-upgrade migration Job (doc 19 §4) → post-sync smoke → auto-rollback on hook failure (`helm rollback`, safe one version per NFR-51).
Remote-site pollers: same registry, `netinv-poller` chart per site cluster (or compose `docker compose pull && up -d` via cron/watchtower-style for non-k8s sites) — core tolerates ±1 version skew (doc 10 §3).

## 4. Secrets & access

GitHub side holds **no** cluster credentials; GHCR pull secrets in-cluster. Actions permissions: default read-only `GITHUB_TOKEN`, `id-token: write` only in build.yml (cosign), environments `staging`/`prod` with required reviewers on prod. Dependabot (Go, npm, Actions, base images) weekly, auto-merge patch-level on green CI.

## 5. Branch protection & quality gates summary

`main`: PR required · CI + E2E status checks · linear history · no force push. Merge blocks on: tests, coverage bars, lint/arch boundaries, security scans, OpenAPI drift, bundle budget, helm lint. Release blocks additionally on: soak/chaos-lite pass (doc 24 §4), security checklist (doc 20 §12), CHANGELOG + docs status flip for shipped features.

## 6. Developer loop (solo + AI)

`make dev` = compose stack + hot-reload (air for Go, Vite) + snmpsim + seeded data — identical images/config to CI, so "works locally" ≈ "passes CI". `make test` / `make lint` mirror CI exactly (same versions, pinned in `tools.go`/`.tool-versions`). An AI agent's inner loop is: edit → `make lint test` → targeted E2E — everything reproducible from the repo (NFR-73).
